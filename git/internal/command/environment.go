package command

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct{ Key, Value string }

func Environment(config ...Config) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "SSH_ASKPASS=") && !strings.HasPrefix(entry, "LC_ALL=") {
			env = append(env, entry)
		}
	}
	config = append([]Config{{"core.hooksPath", os.DevNull}, {"commit.gpgSign", "false"}, {"tag.gpgSign", "false"}}, config...)
	env = append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for index, pair := range config {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, pair.Key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, pair.Value))
	}
	return env
}
