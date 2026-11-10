# Downloading a shared file, version 1

`blocks.json` is the shared oracle for fetching a file the agent offered
(`ToolCall.shared_files`) with `GetSharedFileChunk`. Every response's `data`
is exactly `blockBytes` long, the machine's
`MachineCapabilities.shared_file_chunk_bytes`; a client asks for each offset
in `offsets`, keeps the first `lastBlockFileBytes` bytes of the last block
(the rest is zero padding) and checks the SHA-256 of the result against
`SharedFile.sha256`. An empty file is still one block of zeros. Offsets in
`invalidOffsets` are refused. 64-bit values are decimal strings.

Cases up to three blocks give `sha256` for content where byte `i` is
`i % 251`. The daemon checks the vectors in
`internal/daemon/shared_files_vectors_test.go`; clients check their
downloader against the same file once they implement it.
