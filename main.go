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

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

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
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

const (
	initSucceeded       = 0
	initInvalidArgument = 1
	initIncompatibleABI = 2
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, pluginAPI *C.cliproxy_plugin_api) C.int {
	if host == nil || pluginAPI == nil {
		return initInvalidArgument
	}
	if uint32(host.abi_version) != pluginabi.ABIVersion {
		return initIncompatibleABI
	}

	hostMu.Lock()
	hostCallFn = host.call
	hostFreeFn = host.free_buffer
	hostCtxPtr = host.host_ctx
	hostMu.Unlock()

	pluginAPI.abi_version = C.uint32_t(pluginabi.ABIVersion)
	pluginAPI.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	pluginAPI.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	pluginAPI.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return initSucceeded
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLength C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		return C.int(writeResponse(response, []byte(`{"ok":false,"error":{"code":"invalid_method","message":"method is required"}}`), 1))
	}

	var requestBytes []byte
	if request != nil && requestLength > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLength))
	}
	raw, status := handle(C.GoString(method), requestBytes)
	return C.int(writeResponse(response, raw, status))
}

//export cliproxyPluginFree
func cliproxyPluginFree(pointer unsafe.Pointer, length C.size_t) {
	if pointer != nil {
		C.free(pointer)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	hostMu.Lock()
	hostCallFn = nil
	hostFreeFn = nil
	hostCtxPtr = nil
	hostMu.Unlock()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte, status int) int {
	if response == nil || len(raw) == 0 {
		return 1
	}
	pointer := C.CBytes(raw)
	if pointer == nil {
		return 1
	}
	response.ptr = pointer
	response.len = C.size_t(len(raw))
	return status
}
