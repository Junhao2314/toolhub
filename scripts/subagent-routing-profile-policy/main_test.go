package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	toolskills "github.com/Junhao2314/toolhub/internal/skills"
)

const (
	dispatchID             = "00000000-0000-4000-8000-000000000101"
	subagentID             = "00000000-0000-4000-8000-000000000102"
	reviewID               = "00000000-0000-4000-8000-000000000103"
	legacyID               = "00000000-0000-4000-8000-000000000104"
	routingID              = "00000000-0000-4000-8000-000000000105"
	legacyPolicyID         = "00000000-0000-4000-8000-000000000106"
	toolhubFrontendID      = "00000000-0000-4000-8000-000000000107"
	performanceID          = "00000000-0000-4000-8000-000000000108"
	responsiveID           = "00000000-0000-4000-8000-000000000109"
	uiCNID                 = "00000000-0000-4000-8000-000000000110"
	browserVerificationID  = "00000000-0000-4000-8000-000000000121"
	dispatchVersion        = "00000000-0000-4000-8000-000000000111"
	subagentVersion        = "00000000-0000-4000-8000-000000000112"
	reviewVersion          = "00000000-0000-4000-8000-000000000113"
	legacyPinned           = "00000000-0000-4000-8000-000000000114"
	legacyCurrent          = "00000000-0000-4000-8000-000000000115"
	routingVersion         = "00000000-0000-4000-8000-000000000116"
	toolhubFrontendVersion = "00000000-0000-4000-8000-000000000117"
	performanceVer         = "00000000-0000-4000-8000-000000000118"
	responsiveVer          = "00000000-0000-4000-8000-000000000119"
	uiCNVersion            = "00000000-0000-4000-8000-000000000120"
	browserVerificationVer = "00000000-0000-4000-8000-000000000122"
)

var ordinaryProfiles = []string{
	"claude-coding", "codex-coding",
	"claude-data-analysis", "codex-data-analysis",
	"claude-frontend-ui", "codex-frontend-ui",
}

func TestPlanComputesExactIdempotentDelta(t *testing.T) {
	server := newPolicyServer(t)
	report, err := execute(context.Background(), server.config(modePlan))
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "plan" || report.Skill.Action != "create" || report.Verified {
		t.Fatalf("unexpected plan report: %+v", report)
	}
	if len(report.Profiles) != len(ordinaryProfiles) {
		t.Fatalf("got %d Profile changes, want %d", len(report.Profiles), len(ordinaryProfiles))
	}
	for _, change := range report.Profiles {
		wantAdd := []string{}
		wantRemove := []string{}
		switch change.Name {
		case "claude-coding", "codex-coding":
			wantAdd = []string{"subagent-routing"}
		case "claude-frontend-ui", "codex-frontend-ui":
			wantAdd = append([]string{}, frontendBundleSkillSlugs...)
			wantRemove = []string{"legacy-pinned", "toolhub-frontend"}
		}
		if !reflect.DeepEqual(change.Add, wantAdd) || !reflect.DeepEqual(change.Remove, wantRemove) {
			t.Fatalf("unexpected delta for %s: add=%v remove=%v", change.Name, change.Add, change.Remove)
		}
		if len(wantAdd) == 0 && change.AfterRevision != change.BeforeRevision {
			continue
		}
		if change.AfterRevision == change.BeforeRevision {
			continue
		}
		if change.AfterRevision != change.BeforeRevision+1 {
			t.Fatalf("unexpected revisions for %s: %+v", change.Name, change)
		}
	}
	if server.uploadCount != 0 || server.putCount() != 0 {
		t.Fatalf("plan mutated server: uploads=%d puts=%d", server.uploadCount, server.putCount())
	}
}

func TestApplyRepairsMissingReviewSkillBeforeFrontendBundle(t *testing.T) {
	server := newPolicyServer(t)
	for _, name := range []string{"claude-frontend-ui", "codex-frontend-ui"} {
		current := server.profiles[name]
		pins := make([]skillPin, 0, len(current.Skills))
		ids := make([]string, 0, len(current.SkillIDs))
		for _, pin := range current.Skills {
			if pin.Slug == "requesting-code-review" {
				continue
			}
			pins = append(pins, pin)
			ids = append(ids, pin.SkillID)
		}
		current.Skills = pins
		current.SkillIDs = ids
		server.profiles[name] = current
	}

	report, err := execute(context.Background(), server.config(modeApply))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verified {
		t.Fatalf("repair was not verified: %+v", report)
	}
	for _, name := range []string{"claude-frontend-ui", "codex-frontend-ui"} {
		input := server.lastInput(t, name)
		if len(input.SkillIDs) == 0 || input.SkillIDs[0] != reviewID || input.SkillVersionIDs[reviewID] != reviewVersion {
			t.Fatalf("review Skill was not repaired first for %s: %+v", name, input)
		}
	}
}

func TestApplyUploadsCanonicalSkillAndPreservesEveryExistingPin(t *testing.T) {
	server := newPolicyServer(t)
	report, err := execute(context.Background(), server.config(modeApply))
	if err != nil {
		t.Fatal(err)
	}
	if report.Skill.Action != "create" || !report.Verified {
		t.Fatalf("unexpected apply report: %+v", report)
	}
	if server.uploadCount != 1 || server.uploadSHA != server.pkg.SHA256 {
		t.Fatalf("upload mismatch: count=%d sha=%s want=%s", server.uploadCount, server.uploadSHA, server.pkg.SHA256)
	}
	if server.putCount() != 4 {
		t.Fatalf("got %d PUTs, want 4", server.putCount())
	}

	claude := server.lastInput(t, "claude-coding")
	if claude.SkillVersionIDs[legacyID] != legacyPinned {
		t.Fatalf("non-current Skill pin changed: %+v", claude.SkillVersionIDs)
	}
	if claude.SkillVersionIDs[routingID] != routingVersion {
		t.Fatalf("new Skill pin missing: %+v", claude.SkillVersionIDs)
	}

	for _, name := range []string{"claude-frontend-ui", "codex-frontend-ui"} {
		input := server.lastInput(t, name)
		wantIDs := []string{reviewID}
		for _, slug := range frontendBundleSkillSlugs {
			for _, item := range server.library {
				if item.Slug == slug {
					wantIDs = append(wantIDs, item.ID)
				}
			}
		}
		if !reflect.DeepEqual(input.SkillIDs, wantIDs) {
			t.Fatalf("non-coding membership changed unexpectedly for %s: got=%v want=%v", name, input.SkillIDs, wantIDs)
		}
	}
	uploadsBefore, putsBefore := server.uploadCount, server.putCount()
	second, err := execute(context.Background(), server.config(modeApply))
	if err != nil {
		t.Fatal(err)
	}
	if second.Skill.Action != "reuse" || len(changedProfiles(second.Profiles)) != 0 || !second.Verified {
		t.Fatalf("rerun was not idempotent: %+v", second)
	}
	if server.uploadCount != uploadsBefore || server.putCount() != putsBefore {
		t.Fatalf("rerun mutated state: uploads %d->%d puts %d->%d", uploadsBefore, server.uploadCount, putsBefore, server.putCount())
	}
}

func TestApplyRejectsExistingSlugWithDifferentCanonicalSHA(t *testing.T) {
	server := newPolicyServer(t)
	server.installRoutingSkill("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	report, err := execute(context.Background(), server.config(modeApply))
	if err == nil || !strings.Contains(err.Error(), "collision") || !strings.Contains(err.Error(), server.pkg.SHA256) || !strings.Contains(err.Error(), strings.Repeat("f", 64)) {
		t.Fatalf("expected collision with both hashes, report=%+v err=%v", report, err)
	}
	if report.Skill.Action != "collision" || server.uploadCount != 0 || server.putCount() != 0 {
		t.Fatalf("collision was not fail-closed: report=%+v uploads=%d puts=%d", report, server.uploadCount, server.putCount())
	}
	for _, requestedPath := range server.requestedPaths() {
		if requestedPath == "/api/v1/profiles" {
			t.Fatal("collision detection continued into Profile reads")
		}
	}
}

func TestApplyResumesAfterSomeProfilesAlreadyAdvanced(t *testing.T) {
	server := newPolicyServer(t)
	server.installRoutingSkill(server.pkg.SHA256)
	server.convergeProfile("claude-coding")

	report, err := execute(context.Background(), server.config(modeApply))
	if err != nil {
		t.Fatal(err)
	}
	if report.Skill.Action != "reuse" || !report.Verified {
		t.Fatalf("unexpected resumed report: %+v", report)
	}
	if server.hasPut("claude-coding") {
		t.Fatalf("already-converged Profiles were updated: %+v", server.puts)
	}
	if server.putCount() != 3 {
		t.Fatalf("got %d resumed PUTs, want 3", server.putCount())
	}
}

func TestApplyStopsOnRevisionConflict(t *testing.T) {
	server := newPolicyServer(t)
	server.installRoutingSkill(server.pkg.SHA256)
	server.conflictProfile = "codex-coding"

	report, err := execute(context.Background(), server.config(modeApply))
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "state_conflict") {
		t.Fatalf("expected surfaced revision conflict, report=%+v err=%v", report, err)
	}
	if !server.hasPut("claude-coding") || !server.hasPut("codex-coding") {
		t.Fatalf("expected first update then conflict: %+v", server.puts)
	}
	for _, name := range []string{"codex-data-analysis", "codex-frontend-ui"} {
		if server.hasPut(name) {
			t.Fatalf("updated %s after revision conflict", name)
		}
	}
	for _, change := range report.Profiles {
		wantAfter := change.BeforeRevision
		if change.Name == "claude-coding" || change.Name == "claude-frontend-ui" {
			wantAfter++
		}
		if change.AfterRevision != wantAfter {
			t.Fatalf("partial report falsely advanced %s: %+v", change.Name, change)
		}
	}
}

func TestCheckRejectsMissingMandatoryMembership(t *testing.T) {
	server := newPolicyServer(t)
	server.installRoutingSkill(server.pkg.SHA256)
	server.convergeProfile("claude-coding")
	server.convergeProfile("codex-coding")

	report, err := execute(context.Background(), server.config(modeCheck))
	if err == nil || !strings.Contains(err.Error(), "frontend Kimi Skill") {
		t.Fatalf("expected strict missing-membership error, report=%+v err=%v", report, err)
	}
	if report.Verified || server.uploadCount != 0 || server.putCount() != 0 {
		t.Fatalf("check mutated or verified invalid state: report=%+v", report)
	}
}

func TestCommandNeverCallsPreflightOrApplyRoutes(t *testing.T) {
	server := newPolicyServer(t)
	if _, err := execute(context.Background(), server.config(modeApply)); err != nil {
		t.Fatal(err)
	}
	paths := server.requestedPaths()
	for index, requestedPath := range paths {
		if strings.Contains(requestedPath, "/preflight") || strings.HasSuffix(requestedPath, "/apply") || strings.Contains(requestedPath, "/targets") || strings.Contains(requestedPath, "/operations") {
			t.Fatalf("forbidden route called: %s", requestedPath)
		}
		if strings.HasPrefix(requestedPath, "/api/v1/profiles/") && index > 0 && paths[index-1] != "/api/v1/profiles" {
			t.Fatalf("Profile PUT was not preceded by a fresh Profile list: %v", paths[index-1:index+1])
		}
	}
}

func TestPlanUsesCurrentActiveProfilesDynamically(t *testing.T) {
	server := newPolicyServer(t)
	server.profiles["unexpected-profile"] = profile{
		ID:       "00000000-0000-4000-8000-000000000398",
		Name:     "unexpected-profile",
		Revision: 1,
		SkillIDs: []string{reviewID},
		Skills:   []skillPin{{SkillID: reviewID, VersionID: reviewVersion, Slug: "requesting-code-review"}},
	}

	report, err := execute(context.Background(), server.config(modePlan))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Profiles) != len(ordinaryProfiles)+1 {
		t.Fatalf("dynamic active Profile was not included: %+v", report.Profiles)
	}
}

func TestPlanRemovesLegacyPolicyButKeepsAegisSkills(t *testing.T) {
	server := newPolicyServer(t)
	server.installLegacyPolicySkill(server.pkg.SHA256)
	for name, current := range server.profiles {
		current.SkillIDs = append(current.SkillIDs, legacyPolicyID)
		current.Skills = append(current.Skills, skillPin{SkillID: legacyPolicyID, VersionID: routingVersion, Slug: "same-model-subagents"})
		server.profiles[name] = current
	}
	report, err := execute(context.Background(), server.config(modePlan))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range report.Profiles {
		if !containsString(change.Remove, "same-model-subagents") {
			t.Fatalf("legacy policy was not removed from %s: %+v", change.Name, change)
		}
	}
}

type recordedRequest struct {
	Method string
	Path   string
}

type policyServer struct {
	t               *testing.T
	server          *httptest.Server
	pkg             toolskills.Package
	mu              sync.Mutex
	library         []skill
	profiles        map[string]profile
	requests        []recordedRequest
	puts            map[string][]profileInput
	uploadCount     int
	uploadSHA       string
	conflictProfile string
}

func newPolicyServer(t *testing.T) *policyServer {
	t.Helper()
	skillDir := testSkillDir(t)
	pkg, err := toolskills.ScanDirectory(skillDir, toolskills.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	server := &policyServer{
		t:        t,
		pkg:      pkg,
		library:  baseLibrary(),
		profiles: baseProfiles(),
		puts:     map[string][]profileInput{},
	}
	server.server = httptest.NewServer(http.HandlerFunc(server.handle))
	t.Cleanup(server.server.Close)
	return server
}

func (s *policyServer) config(mode commandMode) commandConfig {
	return commandConfig{
		BaseURL:  s.server.URL,
		Username: "admin",
		Password: "correct-horse-battery-staple",
		SkillDir: testSkillDir(s.t),
		Mode:     mode,
	}
}

func (s *policyServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, recordedRequest{Method: r.Method, Path: r.URL.Path})

	if r.URL.Path == "/api/v1/auth/login" && r.Method == http.MethodPost {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.t.Errorf("decode login: %v", err)
		}
		if body["username"] != "admin" || body["password"] != "correct-horse-battery-staple" {
			s.t.Errorf("unexpected login body: %+v", body)
		}
		http.SetCookie(w, &http.Cookie{Name: "toolhub_session", Value: "session", Path: "/"})
		writeTestJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrfToken": "csrf-token"})
		return
	}
	if cookie, err := r.Cookie("toolhub_session"); err != nil || cookie.Value != "session" {
		writeTestError(w, http.StatusUnauthorized, "unauthenticated", "missing session")
		return
	}
	if r.Method != http.MethodGet && r.Header.Get("X-CSRF-Token") != "csrf-token" {
		writeTestError(w, http.StatusForbidden, "csrf_invalid", "missing CSRF")
		return
	}

	switch {
	case r.URL.Path == "/api/v1/skills" && r.Method == http.MethodGet:
		writeTestJSON(w, http.StatusOK, map[string]any{"items": s.library})
	case r.URL.Path == "/api/v1/skills/upload" && r.Method == http.MethodPost:
		s.handleUpload(w, r)
	case r.URL.Path == "/api/v1/profiles" && r.Method == http.MethodGet:
		items := make([]profile, 0, len(s.profiles))
		names := append([]string{}, ordinaryProfiles...)
		for name := range s.profiles {
			if !containsString(names, name) {
				names = append(names, name)
			}
		}
		for _, name := range names {
			item := cloneProfile(s.profiles[name])
			items = append(items, item)
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"items": items})
	case strings.HasPrefix(r.URL.Path, "/api/v1/profiles/") && r.Method == http.MethodPut:
		s.handleProfilePut(w, r)
	default:
		writeTestError(w, http.StatusNotFound, "not_found", "unexpected route")
	}
}

func (s *policyServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		s.t.Errorf("read multipart upload: %v", err)
		writeTestError(w, http.StatusBadRequest, "invalid_archive", "file missing")
		return
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		s.t.Errorf("read uploaded archive: %v", err)
	}
	pkg, err := toolskills.ScanZIP(body, toolskills.DefaultLimits)
	if err != nil {
		s.t.Errorf("scan uploaded archive: %v", err)
		writeTestError(w, http.StatusBadRequest, "unsafe_archive", err.Error())
		return
	}
	s.uploadCount++
	s.uploadSHA = pkg.SHA256
	item := skill{ID: routingID, Slug: pkg.Slug, CurrentVersionID: routingVersion, CurrentSHA256: pkg.SHA256}
	s.library = append(s.library, item)
	writeTestJSON(w, http.StatusCreated, item)
}

func (s *policyServer) handleProfilePut(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/profiles/")
	var input profileInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.t.Errorf("decode Profile PUT: %v", err)
		writeTestError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	currentName := ""
	for name, item := range s.profiles {
		if item.ID == id {
			currentName = name
			break
		}
	}
	if currentName == "" {
		writeTestError(w, http.StatusNotFound, "not_found", "Profile missing")
		return
	}
	s.puts[currentName] = append(s.puts[currentName], cloneInput(input))
	if currentName == s.conflictProfile {
		writeTestError(w, http.StatusConflict, "state_conflict", "state conflict")
		return
	}
	current := s.profiles[currentName]
	if input.Revision != current.Revision {
		writeTestError(w, http.StatusConflict, "state_conflict", "stale revision")
		return
	}
	updated := profile{
		ID:            current.ID,
		Name:          input.Name,
		Description:   input.Description,
		Revision:      current.Revision + 1,
		CanonicalHash: fmt.Sprintf("hash-%s-%d", currentName, current.Revision+1),
		SkillIDs:      append([]string{}, input.SkillIDs...),
		Skills:        s.pinsFor(input),
	}
	s.profiles[currentName] = updated
	writeTestJSON(w, http.StatusOK, updated)
}

func (s *policyServer) pinsFor(input profileInput) []skillPin {
	result := make([]skillPin, 0, len(input.SkillIDs))
	for _, id := range input.SkillIDs {
		result = append(result, skillPin{SkillID: id, VersionID: input.SkillVersionIDs[id], Slug: s.slugFor(id)})
	}
	return result
}

func (s *policyServer) slugFor(id string) string {
	for _, item := range s.library {
		if item.ID == id {
			return item.Slug
		}
	}
	return ""
}

func (s *policyServer) installRoutingSkill(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.library {
		if item.Slug == "subagent-routing" {
			return
		}
	}
	s.library = append(s.library, skill{ID: routingID, Slug: "subagent-routing", CurrentVersionID: routingVersion, CurrentSHA256: sha})
}

func (s *policyServer) installLegacyPolicySkill(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.library = append(s.library, skill{ID: legacyPolicyID, Slug: "same-model-subagents", CurrentVersionID: routingVersion, CurrentSHA256: sha})
}

func (s *policyServer) convergeProfile(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.profiles[name]
	ids := []string{}
	pins := []skillPin{}
	for _, pin := range current.Skills {
		ids = append(ids, pin.SkillID)
		pins = append(pins, pin)
	}
	if !containsString(ids, routingID) {
		ids = append(ids, routingID)
		pins = append(pins, skillPin{SkillID: routingID, VersionID: routingVersion, Slug: "subagent-routing"})
	}
	current.SkillIDs = ids
	current.Skills = pins
	current.Revision++
	current.CanonicalHash = fmt.Sprintf("converged-%s", name)
	s.profiles[name] = current
}

func (s *policyServer) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, values := range s.puts {
		count += len(values)
	}
	return count
}

func (s *policyServer) hasPut(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.puts[name]) > 0
}

func (s *policyServer) lastInput(t *testing.T, name string) profileInput {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	values := s.puts[name]
	if len(values) == 0 {
		t.Fatalf("no PUT recorded for %s", name)
	}
	return cloneInput(values[len(values)-1])
}

func (s *policyServer) requestedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, 0, len(s.requests))
	for _, item := range s.requests {
		result = append(result, item.Path)
	}
	return result
}

func baseLibrary() []skill {
	return []skill{
		{ID: dispatchID, Slug: "dispatching-parallel-agents", CurrentVersionID: dispatchVersion, CurrentSHA256: strings.Repeat("a", 64)},
		{ID: subagentID, Slug: "subagent-driven-development", CurrentVersionID: subagentVersion, CurrentSHA256: strings.Repeat("b", 64)},
		{ID: reviewID, Slug: "requesting-code-review", CurrentVersionID: reviewVersion, CurrentSHA256: strings.Repeat("c", 64)},
		{ID: legacyID, Slug: "legacy-pinned", CurrentVersionID: legacyCurrent, CurrentSHA256: strings.Repeat("d", 64)},
		{ID: toolhubFrontendID, Slug: "toolhub-frontend", CurrentVersionID: toolhubFrontendVersion, CurrentSHA256: strings.Repeat("e", 64)},
		{ID: performanceID, Slug: "performance-audit", CurrentVersionID: performanceVer, CurrentSHA256: strings.Repeat("f", 64)},
		{ID: responsiveID, Slug: "responsive-check", CurrentVersionID: responsiveVer, CurrentSHA256: strings.Repeat("1", 64)},
		{ID: uiCNID, Slug: "ui-ux-pro-max-cn", CurrentVersionID: uiCNVersion, CurrentSHA256: strings.Repeat("2", 64)},
		{ID: browserVerificationID, Slug: "browser-ui-verification", CurrentVersionID: browserVerificationVer, CurrentSHA256: strings.Repeat("3", 64)},
	}
}

func baseProfiles() map[string]profile {
	result := map[string]profile{}
	for index, name := range ordinaryProfiles {
		pins := []skillPin{
			{SkillID: reviewID, VersionID: reviewVersion, Slug: "requesting-code-review"},
			{SkillID: legacyID, VersionID: legacyPinned, Slug: "legacy-pinned"},
		}
		if name == "claude-coding" {
			pins = []skillPin{
				{SkillID: reviewID, VersionID: reviewVersion, Slug: "requesting-code-review"},
				{SkillID: dispatchID, VersionID: dispatchVersion, Slug: "dispatching-parallel-agents"},
				{SkillID: subagentID, VersionID: subagentVersion, Slug: "subagent-driven-development"},
				{SkillID: legacyID, VersionID: legacyPinned, Slug: "legacy-pinned"},
			}
		}
		if name == "codex-coding" {
			pins = []skillPin{
				{SkillID: reviewID, VersionID: reviewVersion, Slug: "requesting-code-review"},
				{SkillID: dispatchID, VersionID: dispatchVersion, Slug: "dispatching-parallel-agents"},
				{SkillID: subagentID, VersionID: subagentVersion, Slug: "subagent-driven-development"},
				{SkillID: legacyID, VersionID: legacyPinned, Slug: "legacy-pinned"},
			}
		}
		if strings.HasSuffix(name, "-frontend-ui") {
			pins = append(pins, skillPin{SkillID: toolhubFrontendID, VersionID: toolhubFrontendVersion, Slug: "toolhub-frontend"})
		}
		item := profile{
			ID:            fmt.Sprintf("00000000-0000-4000-8000-%012d", 301+index),
			Name:          name,
			Description:   "Profile " + name,
			Revision:      int64(index + 1),
			CanonicalHash: "hash-" + name,
			Skills:        pins,
		}
		for _, pin := range pins {
			item.SkillIDs = append(item.SkillIDs, pin.SkillID)
		}
		result[name] = item
	}
	return result
}

func cloneProfile(input profile) profile {
	input.SkillIDs = append([]string{}, input.SkillIDs...)
	input.Skills = append([]skillPin{}, input.Skills...)
	return input
}

func cloneInput(input profileInput) profileInput {
	input.SkillIDs = append([]string{}, input.SkillIDs...)
	input.SkillVersionIDs = cloneMap(input.SkillVersionIDs)
	return input
}

func cloneMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func changedProfiles(input []profileChange) []profileChange {
	result := []profileChange{}
	for _, item := range input {
		if len(item.Add) > 0 || len(item.Remove) > 0 {
			result = append(result, item)
		}
	}
	return result
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testSkillDir(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".agents", "skills", "subagent-routing"))
}

func writeTestJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeTestError(w http.ResponseWriter, status int, code, message string) {
	writeTestJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "requestId": "test-request"}})
}
