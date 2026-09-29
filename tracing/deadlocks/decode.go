package deadlocks

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/flanksource/commons/logger"
)

// idTimeLayout is the timestamp half of a deadlock id: compact, URL-safe, and
// millisecond precise, which is the precision system_health stamps events with.
const idTimeLayout = "20060102T150405.000Z"

// DecodeEvent decodes one system_health event as fn_xe_file_target_read_file
// returns it: the <event> envelope, whose timestamp dates the deadlock and
// whose xml_report data carries the graph.
func DecodeEvent(eventXML string) (Graph, error) {
	var event struct {
		Name      string `xml:"name,attr"`
		Timestamp string `xml:"timestamp,attr"`
		Data      []struct {
			Name  string `xml:"name,attr"`
			Value struct {
				Inner string `xml:",innerxml"`
			} `xml:"value"`
		} `xml:"data"`
	}
	if err := xml.Unmarshal([]byte(eventXML), &event); err != nil {
		return Graph{}, fmt.Errorf("decode system_health event: %w", err)
	}
	if event.Name != "xml_deadlock_report" {
		return Graph{}, fmt.Errorf("system_health event %q is not an xml_deadlock_report", event.Name)
	}
	at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return Graph{}, fmt.Errorf("xml_deadlock_report timestamp %q: %w", event.Timestamp, err)
	}
	for _, data := range event.Data {
		if data.Name == "xml_report" {
			return DecodeReport(at, data.Value.Inner)
		}
	}
	return Graph{}, fmt.Errorf("xml_deadlock_report at %s carries no xml_report data", event.Timestamp)
}

type xmlReport struct {
	XMLName xml.Name `xml:"deadlock"`
	Victims []struct {
		ID string `xml:"id,attr"`
	} `xml:"victim-list>victimProcess"`
	Processes    []xmlProcess `xml:"process-list>process"`
	ResourceList struct {
		Resources []xmlResource `xml:",any"`
	} `xml:"resource-list"`
}

type xmlProcess struct {
	Attrs       []xml.Attr `xml:",any,attr"`
	Frames      []xmlFrame `xml:"executionStack>frame"`
	InputBuffer string     `xml:"inputbuf"`
}

type xmlFrame struct {
	Attrs []xml.Attr `xml:",any,attr"`
	Text  string     `xml:",chardata"`
}

type xmlResource struct {
	XMLName xml.Name
	Attrs   []xml.Attr       `xml:",any,attr"`
	Owners  []xmlLockRequest `xml:"owner-list>owner"`
	Waiters []xmlLockRequest `xml:"waiter-list>waiter"`
}

type xmlLockRequest struct {
	ID          string `xml:"id,attr"`
	Mode        string `xml:"mode,attr"`
	RequestType string `xml:"requestType,attr"`
}

// DecodeReport decodes a <deadlock> report taken at at. The report is kept
// verbatim as the graph's XML, which is the shape SSMS opens as an .xdl.
func DecodeReport(at time.Time, reportXML string) (Graph, error) {
	reportXML = strings.TrimSpace(reportXML)
	var report xmlReport
	if err := xml.Unmarshal([]byte(reportXML), &report); err != nil {
		return Graph{}, fmt.Errorf("decode deadlock report at %s (expected a <deadlock> root): %w", at.Format(time.RFC3339Nano), err)
	}
	if len(report.Processes) == 0 {
		return Graph{}, fmt.Errorf("deadlock report at %s lists no processes", at.Format(time.RFC3339Nano))
	}

	victims := make(map[string]bool, len(report.Victims))
	for _, victim := range report.Victims {
		victims[victim.ID] = true
	}
	processes := make([]Process, len(report.Processes))
	for i, raw := range report.Processes {
		process, err := decodeProcess(raw, victims, at.UTC())
		if err != nil {
			return Graph{}, fmt.Errorf("deadlock report at %s: %w", at.Format(time.RFC3339Nano), err)
		}
		processes[i] = process
	}
	for _, victim := range report.Victims {
		if !slices.ContainsFunc(processes, func(p Process) bool { return p.ID == victim.ID }) {
			return Graph{}, fmt.Errorf("deadlock report at %s names victim %q, which is not one of its processes",
				at.Format(time.RFC3339Nano), victim.ID)
		}
	}
	resources, err := decodeResources(report.ResourceList.Resources)
	if err != nil {
		return Graph{}, fmt.Errorf("deadlock report at %s: %w", at.Format(time.RFC3339Nano), err)
	}

	deadlock := Deadlock{
		Timestamp: at.UTC(), Victims: len(report.Victims), Participants: len(processes),
		Processes: processes, Resources: resources,
	}
	// The id and the attribution follow the first victim. A report with no
	// victim listed is attributed to its first process, so it still has an id.
	lead := processes[0]
	if len(report.Victims) > 0 {
		lead = processes[slices.IndexFunc(processes, func(p Process) bool { return p.ID == report.Victims[0].ID })]
	}
	deadlock.ID = FormatID(deadlock.Timestamp, lead.ID)
	deadlock.Database = lead.Database
	deadlock.VictimHost = lead.Host
	deadlock.VictimApp = lead.ClientApp
	deadlock.VictimStatement = collapseWhitespace(lead.Statement)
	return Graph{Deadlock: deadlock, XML: reportXML}, nil
}

// FormatID builds a deadlock id from its report timestamp and lead process.
func FormatID(at time.Time, processID string) string {
	return strings.Replace(at.UTC().Format(idTimeLayout), ".", "", 1) + "-" + processID
}

var idPattern = regexp.MustCompile(`^(\d{8}T\d{6})(\d{3})Z-(.+)$`)

// ParseID splits an id from FormatID back into its timestamp and process.
func ParseID(id string) (time.Time, string, error) {
	match := idPattern.FindStringSubmatch(id)
	if match == nil {
		return time.Time{}, "", fmt.Errorf("deadlock id %q is not <yyyymmddThhmmssmmmZ>-<process id>, as `sql deadlocks list` prints it", id)
	}
	at, err := time.Parse(idTimeLayout, match[1]+"."+match[2]+"Z")
	if err != nil {
		return time.Time{}, "", fmt.Errorf("deadlock id %q: %w", id, err)
	}
	return at, match[3], nil
}

func decodeProcess(raw xmlProcess, victims map[string]bool, at time.Time) (Process, error) {
	attrs := attrMap(raw.Attrs)
	process := Process{
		ID: attrs["id"], Victim: victims[attrs["id"]], Status: attrs["status"],
		Host: attrs["hostname"], ClientApp: attrs["clientapp"], Login: attrs["loginname"],
		Database: attrs["currentdbname"], IsolationLevel: attrs["isolationlevel"],
		TransactionName: attrs["transactionname"], LastTranStarted: attrs["lasttranstarted"],
		LockMode: attrs["lockMode"], WaitResource: attrs["waitresource"],
		InputBuffer: strings.TrimSpace(raw.InputBuffer),
	}
	if process.ID == "" {
		return Process{}, fmt.Errorf("a process has no id")
	}
	ints := []struct {
		name   string
		target *int
	}{{"spid", &process.SPID}, {"ecid", &process.ECID}, {"hostpid", &process.HostPID},
		{"trancount", &process.TranCount}, {"priority", &process.Priority}}
	for _, field := range ints {
		value, err := optionalSmallInt(attrs, field.name)
		if err != nil {
			return Process{}, fmt.Errorf("process %s: %w", process.ID, err)
		}
		*field.target = value
	}
	var err error
	if process.WaitTimeMs, err = optionalInt(attrs, "waittime"); err != nil {
		return Process{}, fmt.Errorf("process %s: %w", process.ID, err)
	}
	if process.LogUsed, err = optionalInt(attrs, "logused"); err != nil {
		return Process{}, fmt.Errorf("process %s: %w", process.ID, err)
	}
	if process.WaitTimeMs > 0 {
		since := at.Add(-time.Duration(process.WaitTimeMs) * time.Millisecond)
		process.WaitingSince = &since
	}
	for _, raw := range raw.Frames {
		frame, err := decodeFrame(raw)
		if err != nil {
			return Process{}, fmt.Errorf("process %s frame: %w", process.ID, err)
		}
		process.Frames = append(process.Frames, frame)
	}
	process.sliceStatement()
	return process, nil
}

func decodeFrame(raw xmlFrame) (Frame, error) {
	attrs := attrMap(raw.Attrs)
	frame := Frame{ProcName: attrs["procname"], SQLHandle: attrs["sqlhandle"], Text: strings.TrimSpace(raw.Text)}
	line, err := optionalSmallInt(attrs, "line")
	if err != nil {
		return Frame{}, err
	}
	frame.Line = line
	for _, offset := range []struct {
		name   string
		target **int
	}{{"stmtstart", &frame.StmtStart}, {"stmtend", &frame.StmtEnd}} {
		if attrs[offset.name] == "" {
			continue
		}
		value, err := optionalSmallInt(attrs, offset.name)
		if err != nil {
			return Frame{}, err
		}
		*offset.target = new(value)
	}
	start, end := frame.offsets()
	if start < 0 || start%2 != 0 || end < -1 || (end != -1 && (end%2 != 0 || end < start)) {
		return Frame{}, fmt.Errorf("stmtstart=%d stmtend=%d are not the UTF-16 byte offsets of a statement", start, end)
	}
	return frame, nil
}

// offsets resolves the frame's statement offsets, an omitted one taking SQL
// Server's default: the start of the text, and -1 for its end.
func (f Frame) offsets() (start, end int) {
	start, end = 0, -1
	if f.StmtStart != nil {
		start = *f.StmtStart
	}
	if f.StmtEnd != nil {
		end = *f.StmtEnd
	}
	return start, end
}

// adhocProcName is the procname of a frame running a batch rather than a
// module: only its offsets index the input buffer.
const adhocProcName = "adhoc"

// sliceStatement derives the running statement from the top frame, as
// SUBSTRING(batch, stmtstart/2+1, (stmtend-stmtstart)/2+1) would: the offsets
// count UTF-16 bytes, so the buffer is sliced in UTF-16 code units. The offsets
// count the leading parameter declaration, which the buffer keeps. The buffer
// is trimmed of the newline and indentation the report wraps it in; a batch
// starts on its declaration or first token, and a statement ends on a token,
// so trimming moves neither end of a statement.
func (p *Process) sliceStatement() {
	batch := utf16.Encode([]rune(p.InputBuffer))
	p.Statement, p.StatementLength, p.InputBufferTruncated = p.InputBuffer, len(batch), false
	if len(p.Frames) == 0 || p.Frames[0].ProcName != adhocProcName {
		return
	}
	start, end := p.Frames[0].offsets()
	from, to := start/2, max(len(batch), start/2)
	if end != -1 {
		to = end/2 + 1
	}
	p.StatementLength = to - from
	p.InputBufferTruncated = to > len(batch)
	p.Statement = ""
	if captured := min(to, len(batch)); from < captured {
		p.Statement = string(utf16.Decode(batch[from:captured]))
	}
}

// hobtKinds are the lock resources that sit on an index, identified by the
// hobt (heap or b-tree) id the report carries.
var hobtKinds = map[string]bool{"keylock": true, "pagelock": true, "ridlock": true, "hobtlock": true}

// knownKinds are the resource elements SQL Server writes into a deadlock
// report. A kind outside this set is still kept whole, but logged: it is new
// to this reader, and its attributes are worth a look.
var knownKinds = map[string]bool{
	"keylock": true, "pagelock": true, "ridlock": true, "hobtlock": true, "objectlock": true,
	"extentlock": true, "databaselock": true, "filelock": true, "metadatalock": true,
	"applicationlock": true, "allocunitlock": true, "rowgrouplock": true, "xactlock": true,
	"exchangeEvent": true, "threadpool": true, "resourceWait": true,
}

func decodeResources(raw []xmlResource) ([]Resource, error) {
	var resources []Resource
	byLock := map[string]int{}
	for _, element := range raw {
		resource, err := decodeResource(element)
		if err != nil {
			return nil, err
		}
		if resource.LockID != "" {
			key := resource.Kind + "/" + resource.LockID
			if index, seen := byLock[key]; seen {
				resources[index].Owners = appendDistinct(resources[index].Owners, resource.Owners...)
				resources[index].Waiters = appendDistinct(resources[index].Waiters, resource.Waiters...)
				continue
			}
			byLock[key] = len(resources)
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func decodeResource(element xmlResource) (Resource, error) {
	kind := element.XMLName.Local
	if !knownKinds[kind] {
		logger.Warnf("deadlock report: unrecognised resource kind %q with attributes %v; kept as reported", kind, attrMap(element.Attrs))
	}
	attrs := attrMap(element.Attrs)
	resource := Resource{
		Kind: kind, LockID: attrs["id"], Mode: attrs["mode"], ObjectName: attrs["objectname"],
		Object: lastSegment(attrs["objectname"]), Index: attrs["indexname"],
		Resolution: ResolutionNotApplicable, Attrs: attrs,
		Owners:  appendDistinct(nil, lockRequests(element.Owners)...),
		Waiters: appendDistinct(nil, lockRequests(element.Waiters)...),
	}
	var err error
	if resource.DatabaseID, err = optionalSmallInt(attrs, "dbid"); err != nil {
		return Resource{}, fmt.Errorf("%s %s: %w", kind, resource.LockID, err)
	}
	if resource.FileID, err = optionalSmallInt(attrs, "fileid"); err != nil {
		return Resource{}, fmt.Errorf("%s %s: %w", kind, resource.LockID, err)
	}
	if resource.PageID, err = optionalInt(attrs, "pageid"); err != nil {
		return Resource{}, fmt.Errorf("%s %s: %w", kind, resource.LockID, err)
	}
	if hobtKinds[kind] {
		hobt := attrs["hobtid"]
		if hobt == "" {
			hobt = attrs["associatedObjectId"]
		}
		if resource.HobtID, err = optionalInt(map[string]string{"hobtid": hobt}, "hobtid"); err != nil {
			return Resource{}, fmt.Errorf("%s %s: %w", kind, resource.LockID, err)
		}
		// Until Resolve consults the catalog, an index lock is named only when
		// the report named its index itself.
		resource.Resolution = ResolutionNamed
		if resource.Index == "" {
			resource.Resolution = ResolutionUnresolved
		}
	}
	return resource, nil
}

func lockRequests(raw []xmlLockRequest) []LockRequest {
	out := make([]LockRequest, len(raw))
	for i, request := range raw {
		out[i] = LockRequest{ProcessID: request.ID, Mode: request.Mode, RequestType: request.RequestType}
	}
	return out
}

func appendDistinct(into []LockRequest, requests ...LockRequest) []LockRequest {
	if into == nil {
		into = []LockRequest{}
	}
	for _, request := range requests {
		if !slices.Contains(into, request) {
			into = append(into, request)
		}
	}
	return into
}

func attrMap(attrs []xml.Attr) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		out[attr.Name.Local] = attr.Value
	}
	return out
}

func optionalInt(attrs map[string]string, name string) (int64, error) {
	raw, ok := attrs[name]
	if !ok || raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a number", name, raw)
	}
	return value, nil
}

// optionalSmallInt is optionalInt for an attribute held in an int: ids, counts
// and offsets that never approach the platform's int range.
func optionalSmallInt(attrs map[string]string, name string) (int, error) {
	raw, ok := attrs[name]
	if !ok || raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a number", name, raw)
	}
	return value, nil
}

func lastSegment(objectName string) string {
	if index := strings.LastIndex(objectName, "."); index >= 0 {
		return objectName[index+1:]
	}
	return objectName
}

func collapseWhitespace(s string) string { return strings.Join(strings.Fields(s), " ") }
