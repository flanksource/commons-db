package recordstore_test

import (
	"time"

	"github.com/flanksource/commons/properties"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

// settingsPrefix namespaces the properties these specs set, so none of them
// can read a key a real store uses.
const settingsPrefix = "recordstore.spec"

// setProperty sets one setting for a spec and clears it after, so no spec
// reads a value another left behind.
func setProperty(name, value string) {
	key := settingsPrefix + "." + name
	properties.Set(key, value)
	DeferCleanup(func() { properties.Set(key, "") })
}

var settingDefaults = recordstore.Settings{
	Dir: "/var/lib/records", TTL: 30 * 24 * time.Hour, NDJSONMaxBytes: 256 << 20, NDJSONKeepStreams: 50,
}

var _ = Describe("ReadSettings", func() {
	It("keeps the caller's defaults for every key that is unset", func() {
		settings, err := recordstore.ReadSettings(settingsPrefix, settingDefaults)
		Expect(err).ToNot(HaveOccurred())
		expected := settingDefaults
		expected.Prefix = settingsPrefix
		Expect(settings).To(Equal(expected))
	})

	It("reads every key under the prefix over the defaults", func() {
		setProperty("backend", "ndjson")
		setProperty("dir", "/tmp/records")
		setProperty("ttl", "36h")
		setProperty("ndjson.maxBytes", "1MiB")
		setProperty("ndjson.keepStreams", "3")

		settings, err := recordstore.ReadSettings(settingsPrefix, settingDefaults)
		Expect(err).ToNot(HaveOccurred())
		Expect(settings).To(Equal(recordstore.Settings{
			Prefix: settingsPrefix, Backend: recordstore.BackendNDJSON, Dir: "/tmp/records", TTL: 36 * time.Hour,
			NDJSONMaxBytes: 1 << 20, NDJSONKeepStreams: 3,
		}))
	})

	DescribeTable("refuses a value it cannot use rather than falling back to the default",
		func(name, value, message string) {
			setProperty(name, value)
			_, err := recordstore.ReadSettings(settingsPrefix, settingDefaults)
			Expect(err).To(MatchError(And(ContainSubstring(settingsPrefix+"."+name), ContainSubstring(message))))
		},
		Entry("an unknown backend", "backend", "redis", `"redis"`),
		Entry("a ttl that does not parse", "ttl", "a month", `"a month"`),
		Entry("a ttl that is not positive", "ttl", "0s", `"0s"`),
		Entry("a byte cap that does not parse", "ndjson.maxBytes", "lots", `"lots"`),
		Entry("a byte cap that is not positive", "ndjson.maxBytes", "0", `"0"`),
		Entry("a stream count that does not parse", "ndjson.keepStreams", "many", `"many"`),
		Entry("a stream count that is not positive", "ndjson.keepStreams", "0", `"0"`),
	)

	It("refuses an empty prefix, which would read keys nobody namespaced", func() {
		_, err := recordstore.ReadSettings("", settingDefaults)
		Expect(err).To(MatchError(ContainSubstring("prefix")))
	})
})

var _ = Describe("Settings.Resolve", func() {
	DescribeTable("picks the backend asked for, else kv where there is one, else the fallback",
		func(requested recordstore.BackendKind, hasKV bool, fallback, expected recordstore.BackendKind) {
			settings := recordstore.Settings{Prefix: settingsPrefix, Backend: requested}
			Expect(settings.Resolve(hasKV, fallback)).To(Equal(expected))
		},
		Entry("kv where the caller has one", recordstore.BackendKind(""), true, recordstore.BackendSQLite, recordstore.BackendKV),
		Entry("the sqlite fallback without one", recordstore.BackendKind(""), false, recordstore.BackendSQLite, recordstore.BackendSQLite),
		Entry("the ndjson fallback without one", recordstore.BackendKind(""), false, recordstore.BackendNDJSON, recordstore.BackendNDJSON),
		Entry("an explicit kv with one", recordstore.BackendKV, true, recordstore.BackendNDJSON, recordstore.BackendKV),
		Entry("an explicit sqlite whatever the kv", recordstore.BackendSQLite, true, recordstore.BackendNDJSON, recordstore.BackendSQLite),
		Entry("an explicit ndjson whatever the kv", recordstore.BackendNDJSON, true, recordstore.BackendSQLite, recordstore.BackendNDJSON),
	)

	It("refuses an explicit kv where the caller has none, naming the key", func() {
		_, err := recordstore.Settings{Prefix: settingsPrefix, Backend: recordstore.BackendKV}.Resolve(false, recordstore.BackendSQLite)
		Expect(err).To(MatchError(And(ContainSubstring(settingsPrefix+".backend"), ContainSubstring("no kv store"))))
	})

	It("refuses a kv fallback, which would pick kv exactly when there is none", func() {
		_, err := recordstore.Settings{Prefix: settingsPrefix}.Resolve(false, recordstore.BackendKV)
		Expect(err).To(MatchError(ContainSubstring("fallback")))
	})

	It("refuses a backend it does not know", func() {
		_, err := recordstore.Settings{Prefix: settingsPrefix, Backend: "redis"}.Resolve(true, recordstore.BackendSQLite)
		Expect(err).To(MatchError(ContainSubstring(`"redis"`)))
	})
})
