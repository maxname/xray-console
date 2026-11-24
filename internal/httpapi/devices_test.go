package httpapi

import (
	"net"
	"net/http"
	"testing"
)

func devicesOf(resp response) []any {
	list, _ := resp.body["devices"].([]any)
	return list
}

func TestDeviceIsCheckedThenSetUpAndItsFailureIsKept(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry := h.readyServer("entry")
	set := h.do("POST", "/api/rule-sets", map[string]any{"name": "Дом", "entry_server_id": entry.ID, "default_action": "direct"})
	if set.status != http.StatusCreated {
		t.Fatalf("rule set: %d %s", set.status, set.raw)
	}
	// Any well-formed key will do for one that is never compared.
	hostKey, _ := h.do("GET", "/api/ssh/panel-key", nil).body["public_key"].(string)
	// Nothing listens there: the set-up fails at once, at its first step.
	device := func(change map[string]any) map[string]any {
		body := map[string]any{
			"name": "Роутер дома", "host": "127.0.0.1", "ssh_port": 1, "ssh_user": "root", "host_key": hostKey,
			"password": "", "rule_set_id": id(set), "socks_port": 10808, "http_port": 10809, "lan": false, "auto_push": true,
		}
		for key, value := range change {
			body[key] = value
		}
		return body
	}

	for _, c := range []struct {
		name   string
		change map[string]any
		status int
		code   string
	}{
		{"a host key nobody confirmed", map[string]any{"host_key": ""}, http.StatusUnprocessableEntity, "host_key_required"},
		{"a host key that is not one", map[string]any{"host_key": "ssh-ed25519 AAAA"}, http.StatusUnprocessableEntity, "host_key_required"},
		{"a rule set that is not there", map[string]any{"rule_set_id": 99}, http.StatusUnprocessableEntity, "bad_rule_set"},
		{"both proxies on one port", map[string]any{"http_port": 10808}, http.StatusUnprocessableEntity, "invalid_ports"},
		{"blank name", map[string]any{"name": " "}, http.StatusUnprocessableEntity, "invalid_name"},
		{"shell in the address", map[string]any{"host": "10.0.0.1;reboot"}, http.StatusUnprocessableEntity, "invalid_host"},
	} {
		resp := h.do("POST", "/api/devices", device(c.change))
		if resp.status != c.status || resp.errorCode() != c.code {
			t.Errorf("%s: %d %s", c.name, resp.status, resp.raw)
		}
	}
	if list := h.do("GET", "/api/devices", nil); len(devicesOf(list)) != 0 {
		t.Fatalf("a refused device was stored: %s", list.raw)
	}

	created := h.do("POST", "/api/devices", device(nil))
	if created.status != http.StatusCreated || len(devicesOf(created)) != 1 {
		t.Fatalf("create: %d %s", created.status, created.raw)
	}
	h.devices.Wait()
	list := h.do("GET", "/api/devices", nil)
	dev := devicesOf(list)[0].(map[string]any)
	failure, _ := dev["failure"].(map[string]any)
	if dev["status"] != "failed" || dev["step"] != "connect" || failure["code"] != "ssh_unreachable" {
		t.Fatalf("after a set-up that could not connect: %s", list.raw)
	}
	if dev["rule_set_name"] != "Дом" || dev["pending"] != false {
		t.Errorf("device view: %s", list.raw)
	}
	// The device is a user of the entry server, which now has that to apply.
	if e := dev["entry"].(map[string]any); e["name"] != "entry" || e["pending"] != true {
		t.Errorf("entry: %v", dev["entry"])
	}
	devPath := "/api/devices/" + num(int64(dev["id"].(float64)))

	// What a device stands on cannot be deleted from under it.
	if resp := h.do("DELETE", "/api/rule-sets/"+num(id(set)), nil); resp.status != http.StatusConflict || resp.errorCode() != "rule_set_in_use" {
		t.Errorf("deleting the device's rule set: %d %s", resp.status, resp.raw)
	}

	edited := h.do("PUT", devPath, map[string]any{
		"name": "Роутер", "rule_set_id": id(set), "socks_port": 11080, "http_port": 11081, "lan": true, "auto_push": false,
	})
	if edited.status != http.StatusOK {
		t.Fatalf("edit: %d %s", edited.status, edited.raw)
	}
	if got := devicesOf(edited)[0].(map[string]any); got["name"] != "Роутер" || got["socks_port"] != float64(11080) || got["lan"] != true {
		t.Errorf("after the edit: %v", got)
	}

	// Run again: the same failure, recorded again.
	if resp := h.do("POST", devPath+"/setup", map[string]any{"password": ""}); resp.status != http.StatusOK {
		t.Fatalf("retry: %d %s", resp.status, resp.raw)
	}
	h.devices.Wait()
	if resp := h.do("POST", devPath+"/push", nil); resp.status != http.StatusOK {
		t.Fatalf("push to a device that is not set up: %d %s", resp.status, resp.raw)
	}
	h.devices.Wait()

	removed := h.do("DELETE", devPath, nil)
	if removed.status != http.StatusOK || removed.body["ok"] != true {
		t.Fatalf("delete: %d %s", removed.status, removed.raw)
	}
	if resp := h.do("DELETE", devPath, nil); resp.status != http.StatusNotFound {
		t.Errorf("second delete: %d", resp.status)
	}
	if resp := h.do("DELETE", "/api/rule-sets/"+num(id(set)), nil); resp.status != http.StatusOK {
		t.Errorf("deleting the rule set once the device is gone: %d %s", resp.status, resp.raw)
	}
}

func TestLocalClientIsSwitchedOnAndOffThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry := h.readyServer("entry")
	set := h.do("POST", "/api/rule-sets", map[string]any{"name": "Основной", "entry_server_id": entry.ID, "default_action": "direct"})

	fresh := h.do("GET", "/api/devices", nil)
	local := fresh.body["local"].(map[string]any)
	if local["enabled"] != false || local["state"] != "off" || local["socks_port"] != float64(10808) {
		t.Fatalf("before anything: %s", fresh.raw)
	}

	var ports []int
	for range 2 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
		listener.Close()
	}
	body := map[string]any{"enabled": true, "rule_set_id": 0, "socks_port": ports[0], "http_port": ports[1]}
	if resp := h.do("PUT", "/api/local-client", body); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "bad_rule_set" {
		t.Errorf("on with no rule set: %d %s", resp.status, resp.raw)
	}
	body["rule_set_id"] = id(set)
	body["http_port"] = ports[0]
	if resp := h.do("PUT", "/api/local-client", body); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "invalid_ports" {
		t.Errorf("both on one port: %d %s", resp.status, resp.raw)
	}
	body["http_port"] = ports[1]

	on := h.do("PUT", "/api/local-client", body)
	local, _ = on.body["local"].(map[string]any)
	if on.status != http.StatusOK || local["state"] != "running" || local["enabled"] != true {
		t.Fatalf("switch on: %d %s", on.status, on.raw)
	}
	// The entry server has a new user to be told about.
	if e, _ := local["entry"].(map[string]any); e == nil || e["name"] != "entry" || e["pending"] != true {
		t.Errorf("entry: %v", local["entry"])
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+num(int64(ports[0])))
	if err != nil {
		t.Fatalf("the SOCKS port is not listening: %v", err)
	}
	conn.Close()

	body["enabled"] = false
	off := h.do("PUT", "/api/local-client", body)
	if local, _ = off.body["local"].(map[string]any); off.status != http.StatusOK || local["state"] != "off" {
		t.Fatalf("switch off: %d %s", off.status, off.raw)
	}
	if conn, err := net.Dial("tcp", "127.0.0.1:"+num(int64(ports[0]))); err == nil {
		conn.Close()
		t.Fatal("the SOCKS port still listens after the client was switched off")
	}
}

func TestDeviceEndpointsNeedASession(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)
	if got := h.do("GET", "/api/devices", nil); got.status != http.StatusUnauthorized {
		t.Errorf("list without a session: %d", got.status)
	}
	for _, path := range []string{"/api/devices", "/api/devices/1/setup", "/api/devices/1/push"} {
		if got := h.do("POST", path, map[string]any{}); got.status != http.StatusUnauthorized {
			t.Errorf("POST %s without a session: %d", path, got.status)
		}
	}
	if got := h.do("PUT", "/api/local-client", map[string]any{}); got.status != http.StatusUnauthorized {
		t.Errorf("local client without a session: %d", got.status)
	}
	if got := h.do("DELETE", "/api/devices/1", nil); got.status != http.StatusUnauthorized {
		t.Errorf("delete without a session: %d", got.status)
	}
}

func TestARuleSetTheLocalClientUsesStays(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry := h.readyServer("entry")
	set := h.do("POST", "/api/rule-sets", map[string]any{"name": "Основной", "entry_server_id": entry.ID, "default_action": "direct"})
	path := "/api/rule-sets/" + num(id(set))

	var ports []int
	for range 2 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
		listener.Close()
	}
	body := map[string]any{"enabled": true, "rule_set_id": id(set), "socks_port": ports[0], "http_port": ports[1]}
	if on := h.do("PUT", "/api/local-client", body); on.status != http.StatusOK {
		t.Fatalf("switch on: %d %s", on.status, on.raw)
	}

	// The client is a user of the set, as a subscription would be.
	if resp := h.do("DELETE", path, nil); resp.status != http.StatusConflict || resp.errorCode() != "rule_set_local_client" {
		t.Fatalf("deleting the set the client is on: %d %s", resp.status, resp.raw)
	}

	// Switched off it is nobody's user: the set goes, and the client does not
	// go on naming a set that is not there.
	body["enabled"] = false
	if off := h.do("PUT", "/api/local-client", body); off.status != http.StatusOK {
		t.Fatalf("switch off: %d %s", off.status, off.raw)
	}
	if resp := h.do("DELETE", path, nil); resp.status != http.StatusOK {
		t.Fatalf("deleting the set of a client that is off: %d %s", resp.status, resp.raw)
	}
	after := h.do("GET", "/api/devices", nil)
	local, _ := after.body["local"].(map[string]any)
	if local["rule_set_id"] != float64(0) || local["socks_port"] != float64(ports[0]) {
		t.Fatalf("the client after its set was deleted: %s", after.raw)
	}
}
