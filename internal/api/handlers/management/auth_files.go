// Package management provides the management API handlers and middleware
// for configuring the server and managing auth files.
//
// Auth-file related code is split across:
//   - auth_oauth_infra.go       callback forwarders / webui helpers
//   - auth_files_list.go        list/models/entry builders
//   - auth_files_io.go          upload/download/delete
//   - auth_files_patch.go       patch/status/save helpers
//   - auth_files_gitlab_helpers.go
//   - auth_files_status.go      GetAuthStatus
//   - oauth_handlers_*.go       per-provider OAuth start handlers
//   - oauth_flow_helpers.go     shared OAuth wait/complete helpers
package management
