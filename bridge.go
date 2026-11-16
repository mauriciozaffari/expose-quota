package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

static int call_host(cliproxy_host_call_fn fn, void* ctx, const char* method,
                     const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return fn(ctx, method, request, request_len, response);
}

static void free_host_buffer(cliproxy_host_free_fn fn, void* ptr, size_t len) {
	if (fn != NULL) {
		fn(ptr, len);
	} else {
		free(ptr);
	}
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var (
	hostMu     sync.RWMutex
	hostCallFn C.cliproxy_host_call_fn
	hostFreeFn C.cliproxy_host_free_fn
	hostCtxPtr unsafe.Pointer
)

// hostCall invokes the stored C host callback function for the given RPC method
// and JSON request, returning the JSON response bytes.
func hostCall(method string, request []byte) ([]byte, error) {
	hostMu.RLock()
	defer hostMu.RUnlock()
	if hostCallFn == nil {
		return nil, fmt.Errorf("host callback is not initialized")
	}

	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var cRequest unsafe.Pointer
	if len(request) > 0 {
		cRequest = C.CBytes(request)
		defer C.free(cRequest)
	}

	var response C.cliproxy_buffer
	rc := C.call_host(hostCallFn, hostCtxPtr, cMethod, (*C.uint8_t)(cRequest), C.size_t(len(request)), &response)

	var out []byte
	if response.ptr != nil && response.len > 0 {
		out = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(hostFreeFn, response.ptr, response.len)
	}

	if rc != 0 {
		if len(out) > 0 {
			return out, nil
		}
		return nil, fmt.Errorf("host call %s returned %d", method, int(rc))
	}
	return out, nil
}

type cgoHostBridge struct{}

func (b *cgoHostBridge) ListAuthFiles(_ context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	respBytes, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil, err
	}

	var envelope pluginabi.Envelope
	if err := json.Unmarshal(respBytes, &envelope); err != nil {
		return nil, fmt.Errorf("decode host auth list envelope: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("host auth list: %s", envelope.Error.Message)
	}

	var resp struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if err := json.Unmarshal(envelope.Result, &resp); err == nil && resp.Files != nil {
		return resp.Files, nil
	}

	var files []pluginapi.HostAuthFileEntry
	if err := json.Unmarshal(envelope.Result, &files); err == nil {
		return files, nil
	}

	return nil, fmt.Errorf("decode host auth list result: invalid format")
}

func (b *cgoHostBridge) GetAuthFile(_ context.Context, authIndex string) (pluginapi.HostAuthGetResponse, error) {
	requestJSON, err := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if err != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("marshal host auth get request: %w", err)
	}

	respBytes, err := hostCall(pluginabi.MethodHostAuthGet, requestJSON)
	if err != nil {
		return pluginapi.HostAuthGetResponse{}, err
	}

	var envelope pluginabi.Envelope
	if err := json.Unmarshal(respBytes, &envelope); err != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host auth get envelope: %w", err)
	}
	if envelope.Error != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("host auth get: %s", envelope.Error.Message)
	}

	var result pluginapi.HostAuthGetResponse
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host auth get result: %w", err)
	}
	return result, nil
}
