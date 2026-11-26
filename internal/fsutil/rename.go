package fsutil

import "os"

// Rename moves the file or directory oldpath to newpath, which must be on
// the same file system. dpm swaps deployed directories with it.
//
// It is a variable so that tests can make a swap fail part way and check
// that the destinations are rolled back. Production code must not change it.
var Rename = os.Rename
