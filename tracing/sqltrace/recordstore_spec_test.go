// Checks that the recordresults trace store is a RecordStore a capture can
// commit its rows through.
package sqltrace

import "github.com/flanksource/commons-db/recordstore/recordresults"

var _ RecordStore = recordresults.TraceStore{}
