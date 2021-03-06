package loader

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	log "github.com/hashicorp/go-hclog"
	plugin "github.com/hashicorp/go-plugin"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/device"
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
)

// TestMain runs either the tests or runs a mock plugin based on the passed
// flags
func TestMain(m *testing.M) {
	var plugin, configSchema bool
	var name, pluginType, pluginVersion string
	flag.BoolVar(&plugin, "plugin", false, "run binary as a plugin")
	flag.BoolVar(&configSchema, "config-schema", true, "return a config schema")
	flag.StringVar(&name, "name", "", "plugin name")
	flag.StringVar(&pluginType, "type", "", "plugin type")
	flag.StringVar(&pluginVersion, "version", "", "plugin version")
	flag.Parse()

	if plugin {
		if err := pluginMain(name, pluginType, pluginVersion, configSchema); err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
	} else {
		os.Exit(m.Run())
	}
}

// pluginMain starts a mock plugin using the passed parameters
func pluginMain(name, pluginType, version string, config bool) error {
	// Validate passed parameters
	if name == "" || pluginType == "" {
		return fmt.Errorf("name and plugin type must be specified")
	}

	switch pluginType {
	case base.PluginTypeDevice:
	default:
		return fmt.Errorf("unsupported plugin type %q", pluginType)
	}

	// Create the mock plugin
	m := &mockPlugin{
		name:         name,
		ptype:        pluginType,
		version:      version,
		configSchema: config,
	}

	// Build the plugin map
	pmap := map[string]plugin.Plugin{
		base.PluginTypeBase: &base.PluginBase{Impl: m},
	}
	switch pluginType {
	case base.PluginTypeDevice:
		pmap[base.PluginTypeDevice] = &device.PluginDevice{Impl: m}
	}

	// Serve the plugin
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: base.Handshake,
		Plugins:         pmap,
		GRPCServer:      plugin.DefaultGRPCServer,
	})

	return nil
}

// mockFactory returns a PluginFactory method which creates the mock plugin with
// the passed parameters
func mockFactory(name, ptype, version string, configSchema bool) func(log log.Logger) interface{} {
	return func(log log.Logger) interface{} {
		return &mockPlugin{
			name:         name,
			ptype:        ptype,
			version:      version,
			configSchema: configSchema,
		}
	}
}

// mockPlugin is a plugin that meets various plugin interfaces but is only
// useful for testing.
type mockPlugin struct {
	name         string
	ptype        string
	version      string
	configSchema bool

	// config is built on SetConfig
	config *mockPluginConfig
	// nomadconfig is set on SetConfig
	nomadConfig *base.ClientAgentConfig
}

// mockPluginConfig is the configuration for the mock plugin
type mockPluginConfig struct {
	Foo string `codec:"foo"`
	Bar int    `codec:"bar"`

	// ResKey is a key that is populated in the Env map when a device is
	// reserved.
	ResKey string `codec:"res_key"`
}

// PluginInfo returns the plugin information based on the passed fields when
// building the mock plugin
func (m *mockPlugin) PluginInfo() (*base.PluginInfoResponse, error) {
	return &base.PluginInfoResponse{
		Type:             m.ptype,
		PluginApiVersion: "v0.1.0",
		PluginVersion:    m.version,
		Name:             m.name,
	}, nil
}

func (m *mockPlugin) ConfigSchema() (*hclspec.Spec, error) {
	if !m.configSchema {
		return nil, nil
	}

	// configSpec is the hclspec for parsing the mock's configuration
	configSpec := hclspec.NewObject(map[string]*hclspec.Spec{
		"foo":     hclspec.NewAttr("foo", "string", false),
		"bar":     hclspec.NewAttr("bar", "number", false),
		"res_key": hclspec.NewAttr("res_key", "string", false),
	})

	return configSpec, nil
}

// SetConfig decodes the configuration and stores it
func (m *mockPlugin) SetConfig(data []byte, cfg *base.ClientAgentConfig) error {
	var config mockPluginConfig
	if err := base.MsgPackDecode(data, &config); err != nil {
		return err
	}

	m.config = &config
	m.nomadConfig = cfg
	return nil
}

func (m *mockPlugin) Fingerprint(ctx context.Context) (<-chan *device.FingerprintResponse, error) {
	return make(chan *device.FingerprintResponse), nil
}

func (m *mockPlugin) Reserve(deviceIDs []string) (*device.ContainerReservation, error) {
	if m.config == nil || m.config.ResKey == "" {
		return nil, nil
	}

	return &device.ContainerReservation{
		Envs: map[string]string{m.config.ResKey: "config-set"},
	}, nil
}

func (m *mockPlugin) Stats(ctx context.Context) (<-chan *device.StatsResponse, error) {
	return make(chan *device.StatsResponse), nil
}
