package config

import (
	"context"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
)

// DefaultApprovalsListen is the approval console address when [approvals] sets none.
const DefaultApprovalsListen = "localhost:8788"

// Approvals is the [approvals] table: the console listen address and extra Host names it accepts.
type Approvals struct {
	Listen string
	Hosts  []string
}

// ApprovalsError names the invalid [approvals] key path. It never contains the configured value.
type ApprovalsError struct{ Path string }

func (e ApprovalsError) Error() string { return "invalid approvals: " + e.Path }

// LoadApprovals reads the optional [approvals] table. An absent table or key gives the default.
func LoadApprovals(ctx context.Context, path string) (Approvals, error) {
	d, _, err := document(ctx, path)
	if err != nil {
		return Approvals{}, err
	}
	result := Approvals{Listen: DefaultApprovalsListen}
	value, present := d["approvals"]
	if !present {
		return result, nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return Approvals{}, ApprovalsError{keyPath("approvals")}
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		fail := ApprovalsError{keyPath("approvals", key)}
		switch key {
		case "listen":
			listen, ok := table[key].(string)
			if !ok || !validAddress(listen) {
				return Approvals{}, fail
			}
			result.Listen = listen
		case "hosts":
			list, ok := table[key].([]any)
			if !ok {
				return Approvals{}, fail
			}
			result.Hosts = make([]string, 0, len(list))
			for _, item := range list {
				host, ok := item.(string)
				// A placeholder port lets an entry without a port pass the same check.
				if !ok || (!validAddress(host) && !validAddress(host+":1")) {
					return Approvals{}, fail
				}
				result.Hosts = append(result.Hosts, host)
			}
		default:
			return Approvals{}, fail
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
