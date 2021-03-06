package state

import (
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	trstate "github.com/hashicorp/nomad/client/allocrunner/taskrunner/state"
	"github.com/hashicorp/nomad/nomad/mock"
	"github.com/hashicorp/nomad/nomad/structs"
	"github.com/kr/pretty"
	"github.com/stretchr/testify/require"
)

func setupBoltDB(t *testing.T) (*BoltStateDB, func()) {
	dir, err := ioutil.TempDir("", "nomadtest")
	require.NoError(t, err)

	db, err := NewBoltStateDB(dir)
	if err != nil {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("error removing boltdb dir: %v", err)
		}
		t.Fatalf("error creating boltdb: %v", err)
	}

	cleanup := func() {
		if err := db.Close(); err != nil {
			t.Errorf("error closing boltdb: %v", err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("error removing boltdb dir: %v", err)
		}
	}

	return db.(*BoltStateDB), cleanup
}

func testDB(t *testing.T, f func(*testing.T, StateDB)) {
	boltdb, cleanup := setupBoltDB(t)
	defer cleanup()

	memdb := NewMemDB()

	impls := []StateDB{boltdb, memdb}

	for _, db := range impls {
		db := db
		t.Run(db.Name(), func(t *testing.T) {
			f(t, db)
		})
	}
}

// TestStateDB asserts the behavior of GetAllAllocations, PutAllocation, and
// DeleteAllocationBucket for all operational StateDB implementations.
func TestStateDB_Allocations(t *testing.T) {
	t.Parallel()

	testDB(t, func(t *testing.T, db StateDB) {
		require := require.New(t)

		// Empty database should return empty non-nil results
		allocs, errs, err := db.GetAllAllocations()
		require.NoError(err)
		require.NotNil(allocs)
		require.Empty(allocs)
		require.NotNil(errs)
		require.Empty(errs)

		// Put allocations
		alloc1 := mock.Alloc()
		alloc2 := mock.BatchAlloc()

		//XXX Sadly roundtripping allocs loses time.Duration type
		//    information from the Config map[string]interface{}. As
		//    the mock driver itself with unmarshal run_for into the
		//    proper type, we can safely ignore it here.
		delete(alloc2.Job.TaskGroups[0].Tasks[0].Config, "run_for")

		require.NoError(db.PutAllocation(alloc1))
		require.NoError(db.PutAllocation(alloc2))

		// Retrieve them
		allocs, errs, err = db.GetAllAllocations()
		require.NoError(err)
		require.NotNil(allocs)
		require.Len(allocs, 2)
		for _, a := range allocs {
			switch a.ID {
			case alloc1.ID:
				if !reflect.DeepEqual(a, alloc1) {
					pretty.Ldiff(t, a, alloc1)
					t.Fatalf("alloc %q unequal", a.ID)
				}
			case alloc2.ID:
				if !reflect.DeepEqual(a, alloc2) {
					pretty.Ldiff(t, a, alloc2)
					t.Fatalf("alloc %q unequal", a.ID)
				}
			default:
				t.Fatalf("unexpected alloc id %q", a.ID)
			}
		}
		require.NotNil(errs)
		require.Empty(errs)

		// Add another
		alloc3 := mock.SystemAlloc()
		require.NoError(db.PutAllocation(alloc3))
		allocs, errs, err = db.GetAllAllocations()
		require.NoError(err)
		require.NotNil(allocs)
		require.Len(allocs, 3)
		require.Contains(allocs, alloc1)
		require.Contains(allocs, alloc2)
		require.Contains(allocs, alloc3)
		require.NotNil(errs)
		require.Empty(errs)

		// Deleting a nonexistent alloc is a noop
		require.NoError(db.DeleteAllocationBucket("asdf"))
		allocs, _, err = db.GetAllAllocations()
		require.NoError(err)
		require.NotNil(allocs)
		require.Len(allocs, 3)

		// Delete alloc1
		require.NoError(db.DeleteAllocationBucket(alloc1.ID))
		allocs, errs, err = db.GetAllAllocations()
		require.NoError(err)
		require.NotNil(allocs)
		require.Len(allocs, 2)
		require.Contains(allocs, alloc2)
		require.Contains(allocs, alloc3)
		require.NotNil(errs)
		require.Empty(errs)
	})
}

// TestStateDB_TaskState asserts the behavior of task state related StateDB
// methods.
func TestStateDB_TaskState(t *testing.T) {
	t.Parallel()

	testDB(t, func(t *testing.T, db StateDB) {
		require := require.New(t)

		// Getting nonexistent state should return nils
		ls, ts, err := db.GetTaskRunnerState("allocid", "taskname")
		require.NoError(err)
		require.Nil(ls)
		require.Nil(ts)

		// Putting TaskState without first putting the allocation should work
		state := structs.NewTaskState()
		state.Failed = true // set a non-default value
		require.NoError(db.PutTaskState("allocid", "taskname", state))

		// Getting should return the available state
		ls, ts, err = db.GetTaskRunnerState("allocid", "taskname")
		require.NoError(err)
		require.Nil(ls)
		require.Equal(state, ts)

		// Deleting a nonexistent task should not error
		require.NoError(db.DeleteTaskBucket("adsf", "asdf"))
		require.NoError(db.DeleteTaskBucket("asllocid", "asdf"))

		// Data should be untouched
		ls, ts, err = db.GetTaskRunnerState("allocid", "taskname")
		require.NoError(err)
		require.Nil(ls)
		require.Equal(state, ts)

		// Deleting the task should remove the state
		require.NoError(db.DeleteTaskBucket("allocid", "taskname"))
		ls, ts, err = db.GetTaskRunnerState("allocid", "taskname")
		require.NoError(err)
		require.Nil(ls)
		require.Nil(ts)

		// Putting LocalState should work just like TaskState
		origLocalState := trstate.NewLocalState()
		require.NoError(db.PutTaskRunnerLocalState("allocid", "taskname", origLocalState))
		ls, ts, err = db.GetTaskRunnerState("allocid", "taskname")
		require.NoError(err)
		require.Equal(origLocalState, ls)
		require.Nil(ts)
	})
}
