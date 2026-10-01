// CLIProxyAPI native plugin C ABI, and the entry points in entry.c that wrap it.
#ifndef CLAUDE_KEEP_ROLLING_ABI_H
#define CLAUDE_KEEP_ROLLING_ABI_H

#include <stddef.h>
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

int entry_plugin_call(char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response);
void entry_plugin_free(void* ptr, size_t len);
void entry_plugin_shutdown(void);
void entry_store_host(const cliproxy_host_api* host);
int entry_call_host(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response);
void entry_free_host_buffer(void* ptr, size_t len);

#endif
