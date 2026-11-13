package party

import (
	"strings"
	"testing"
)

func TestTVLoginRequiresAccountSessionAndCSRF(t *testing.T) {
	f := newPartyFixture(t)
	path := "/api/v1/auth/tv-session"
	input := document{"name": "客厅电视"}
	want(t, f.request(t, "POST", path, input, "", true, false), 403)
	for _, name := range []any{"", strings.Repeat("视", 61), "TV\n客厅", 123} {
		want(t, f.request(t, "POST", path, document{"name": name}, "", true, true), 400)
	}
	value := want(t, f.request(t, "POST", path, input, "", true, true), 200)
	credential, deviceID := str(value, "token"), str(value, "deviceId")
	if len(value) != 2 || len(credential) < 40 || deviceID == "" {
		t.Fatal("login did not return an independent TV credential", value)
	}
	var stored, raw string
	if err := f.db.QueryRow("SELECT secret_hash,document FROM tv_record WHERE kind='credential'").Scan(&stored, &raw); err != nil {
		t.Fatal(err)
	}
	if stored != hash(credential) || strings.Contains(raw, credential) {
		t.Fatal("TV credential must only be stored as a digest")
	}
	var pending int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM tv_record WHERE kind='authorization'").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("account login created an authorization request", pending, err)
	}
	want(t, f.request(t, "POST", "/api/v1/auth/logout", nil, "", true, true), 200)
	// The TV credential survives disposal of the temporary account session.
	want(t, f.request(t, "GET", "/api/v1/tv/home", nil, credential, false, false), 200)
	want(t, f.request(t, "GET", "/api/v1/auth/session", nil, credential, false, false), 401)
	// A bearer token or an incorrect password cannot issue another TV login.
	want(t, f.request(t, "POST", path, input, credential, true, true), 401)
	want(t, f.request(t, "POST", "/api/v1/auth/login", document{"username": "admin", "password": "wrong"}, "", true, true), 401)
	want(t, f.request(t, "POST", path, input, "", true, true), 401)
	f.login(t)
	want(t, f.request(t, "DELETE", "/api/v1/admin/tv-devices/"+deviceID, nil, "", true, true), 200)
	want(t, f.request(t, "GET", "/api/v1/tv/home", nil, credential, false, false), 401)
}

func TestTVLoginWaitsForCompletedMFA(t *testing.T) {
	f := newPartyFixture(t)
	sealed, err := f.s.cipher.Encrypt("JBSWY3DPEHPK3PXP", "singverse:totp:v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE auth_account SET mfa_secret=? WHERE id=1", sealed); err != nil {
		t.Fatal(err)
	}
	const recovery = "tv-login-fixture-recovery"
	if _, err = f.db.Exec("INSERT INTO auth_recovery(code_hash) VALUES(?)", hash(recovery)); err != nil {
		t.Fatal(err)
	}
	want(t, f.request(t, "POST", "/api/v1/auth/logout", nil, "", true, true), 200)
	state := want(t, f.request(t, "POST", "/api/v1/auth/login", document{"username": "admin", "password": "admin"}, "", true, true), 200)
	if str(state, "state") != "mfaRequired" {
		t.Fatal("password login bypassed MFA", state)
	}
	input := document{"name": "客厅电视"}
	want(t, f.request(t, "POST", "/api/v1/auth/tv-session", input, "", true, true), 401)
	want(t, f.request(t, "POST", "/api/v1/auth/mfa/verify", document{"code": "wrong-recovery"}, "", true, true), 401)
	want(t, f.request(t, "POST", "/api/v1/auth/tv-session", input, "", true, true), 401)
	want(t, f.request(t, "POST", "/api/v1/auth/mfa/verify", document{"code": recovery}, "", true, true), 200)
	credential := want(t, f.request(t, "POST", "/api/v1/auth/tv-session", input, "", true, true), 200)
	want(t, f.request(t, "GET", "/api/v1/tv/home", nil, str(credential, "token"), false, false), 200)
}

func TestTVAuthorizationManagementRoutesRemoved(t *testing.T) {
	f := newPartyFixture(t)
	for _, path := range []string{
		"/api/v1/device-authorizations",
		"/api/v1/device-authorizations/old/poll",
		"/api/v1/admin/device-authorizations/old/approve",
	} {
		want(t, f.request(t, "POST", path, document{}, "", true, true), 404)
	}
	want(t, f.request(t, "DELETE", "/api/v1/admin/device-authorizations/old", nil, "", true, true), 404)
	want(t, f.request(t, "GET", "/api/v1/admin/tv-devices", nil, "", true, false), 404)
}

func TestExistingTVCredentialSurvivesAccountLoginChange(t *testing.T) {
	f := newPartyFixture(t)
	// Older releases derived the bearer from a polling secret and stored its digest.
	legacy := hash("singverse:device:legacy-poll-fixture")
	if _, err := f.db.Exec("INSERT INTO tv_record(id,kind,document) VALUES(?,?,?)", "legacy-tv", "device", encode(document{"id": "legacy-tv", "name": "原有电视", "revoked": false, "credentialId": "legacy-credential"})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("INSERT INTO tv_record(id,kind,secret_hash,document) VALUES(?,?,?,?)", "legacy-credential", "credential", hash(legacy), encode(document{"deviceId": "legacy-tv"})); err != nil {
		t.Fatal(err)
	}
	f.device(t)
	want(t, f.request(t, "GET", "/api/v1/tv/home", nil, legacy, false, false), 200)
}
