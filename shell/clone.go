package shell

import "errors"

// errCloneUnsupported is returned by cloneFile when the filesystem cannot do a
// copy-on-write clone, telling copyPath to fall back to a byte copy.
var errCloneUnsupported = errors.New("copy-on-write clone unsupported")
