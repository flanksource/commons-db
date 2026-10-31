// Package container follows Docker Engine API logs through a unix socket.
// DOCKER_HOST selects a unix socket; when unset, /var/run/docker.sock is used.
// A Podman Docker-compatible socket can be used with Runtime set to "podman".
// Containerd's native API and TTY streams are not supported.
//
// FollowOptions.Container selects an ID or name, Since bounds the start time,
// Tail passes Docker's tail value, and Follow keeps the stream open. Emit is
// called synchronously for each line and should return promptly. Host defaults
// to the local hostname and Runtime defaults to "docker".
// Lines reaching 64 KiB are emitted in chunks with the label truncated=true.
// Cancellation flushes partial lines and returns the unwrapped context error.
package container
