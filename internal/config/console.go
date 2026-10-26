package config

import (
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
)

// console reads the optional [console] table. An absent table or key gives the default.
func console(d map[string]any) (Console, error) {
	result := Console{Listen: DefaultConsoleListen}
	value, present := d["console"]
	if !present {
		return result, nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return Console{}, fail("must be a table", "console")
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		switch key {
		case "listen":
			listen, ok := table[key].(string)
			if !ok || !validAddress(listen) {
				return Console{}, fail("must be HOST:PORT", "console", key)
			}
			result.Listen = listen
		case "hosts":
			list, ok := table[key].([]any)
			if !ok {
				return Console{}, fail("must be a list of HOST or HOST:PORT", "console", key)
			}
			result.Hosts = make([]string, 0, len(list))
			for _, item := range list {
				host, ok := item.(string)
				// A placeholder port lets an entry without a port pass the same check.
				if !ok || (!validAddress(host) && !validAddress(host+":1")) {
					return Console{}, fail("must be a list of HOST or HOST:PORT", "console", key)
				}
				result.Hosts = append(result.Hosts, host)
			}
		default:
			return Console{}, fail("unknown key", "console", key)
		}
	}
	return result, nil
}

const hostChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-"

// validAddress accepts HOST:PORT with a numeric port from 1 to 65535. HOST is an IP address or a name
// of letters, digits, dots, and hyphens. Only an IPv6 address is in brackets, and it must be.
func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.HasPrefix(address, "[") != strings.Contains(host, ":") {
		return false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	return err == nil && n > 0 && host != "" && (net.ParseIP(host) != nil || strings.Trim(host, hostChars) == "")
}
