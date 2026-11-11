package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef struct { uint32_t abi_version; void* host_ctx; void* call; void* free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
  uint32_t abi_version;
  cliproxy_plugin_call_fn call;
  cliproxy_plugin_free_fn free_buffer;
  cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"unsafe"

	"gopkg.in/yaml.v3"
)

const pluginID = "provider-balances"
const apiPath = "/v0/management/provider-balances"
const resourcePath = "/v0/resource/plugins/provider-balances"

var runtimeState struct {
	sync.RWMutex
	store *providerStore
}

type envelope struct {
	OK     bool `json:"ok"`
	Result any  `json:"result,omitempty"`
	Error  any  `json:"error,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr, response.len = nil, 0
	if method == nil || requestLen > 2<<20 {
		return 1
	}
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	result, err := dispatch(C.GoString(method), raw)
	out := envelope{OK: err == nil, Result: result}
	if err != nil {
		out.Result = nil
		out.Error = map[string]string{"code": "plugin_error", "message": err.Error()}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return 1
	}
	response.ptr, response.len = C.CBytes(body), C.size_t(len(body))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	C.free(ptr)
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func dispatch(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		var request struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, errors.New("invalid plugin configuration")
		}
		var cfg struct {
			DataFile string `yaml:"data_file"`
		}
		if err := yaml.Unmarshal(request.ConfigYAML, &cfg); err != nil {
			return nil, errors.New("invalid plugin configuration")
		}
		if cfg.DataFile == "" {
			cfg.DataFile = "data/provider-balances.json"
		}
		path, err := filepath.Abs(cfg.DataFile)
		if err != nil {
			return nil, errors.New("invalid data_file path")
		}
		runtimeState.Lock()
		defer runtimeState.Unlock()
		if runtimeState.store == nil || runtimeState.store.path != path {
			store, err := openStore(path)
			if err != nil {
				return nil, err
			}
			runtimeState.store = store
		}
		return map[string]any{
			// Schema 6 returns JSON strings unchanged; the UI renders them as text.
			"schema_version": 6,
			"metadata": map[string]any{
				"Name": pluginID, "Version": "0.3.0", "Author": "local",
				"GitHubRepository": "https://github.com/router-for-me/CLIProxyAPI",
				"ConfigFields": []any{map[string]string{
					"Name": "data_file", "Type": "string",
					"Description": "Server file for provider credentials and saved balances. Default: data/provider-balances.json",
				}},
			},
			"capabilities": map[string]bool{"management_api": true},
		}, nil
	case "management.register":
		return map[string]any{
			"routes": []any{
				map[string]string{"Method": "GET", "Path": apiPath + "/providers"},
				map[string]string{"Method": "PUT", "Path": apiPath + "/providers"},
				map[string]string{"Method": "DELETE", "Path": apiPath + "/providers"},
				map[string]string{"Method": "POST", "Path": apiPath + "/refresh"},
			},
			"resources": []any{
				map[string]string{"Path": "/balances", "Menu": "Балансы поставщиков", "Description": "New API: баланс аккаунта по нажатию на кошелёк"},
				map[string]string{"Path": "/app.js"},
				map[string]string{"Path": "/styles.css"},
			},
		}, nil
	case "management.handle":
		var request managementRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, errors.New("invalid management request")
		}
		runtimeState.RLock()
		store := runtimeState.store
		runtimeState.RUnlock()
		return handleManagement(store, request), nil
	default:
		return nil, errors.New("unsupported plugin method")
	}
}
