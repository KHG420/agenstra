package agenstra

import "testing"

func TestEffectiveSettingsSurviveHostChangeAndCheckpoints(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	h.Settings.MaxModelRounds = 7
	run := createTestHostRun(t, h)
	h.Settings.MaxModelRounds = 1
	c, err := h.effectiveRunConfig(run)
	if err != nil || c.Settings.MaxModelRounds != 7 || c.Source != "run_snapshot" {
		t.Fatalf("%+v %v", c, err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c, err = h.effectiveRunConfig(run)
	if err != nil || c.Settings.MaxModelRounds != 7 {
		t.Fatalf("%+v %v", c, err)
	}
	delete(run.State, "effective_config")
	c, err = h.effectiveRunConfig(run)
	if err != nil || c.Source != "current_host" || c.Settings.MaxModelRounds != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	run.State["effective_config"] = JSON{"version": 900}
	if _, err = h.restore(run); ErrorCode(err) != "run_state_invalid" {
		t.Fatal(err)
	}
}

func TestOlderSettingsSnapshotDoesNotInheritNewHostPolicy(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	config, callErr := objectOf(run.State["effective_config"])
	if callErr != nil {
		t.Error(callErr)
	}
	settings := config["settings"].(JSON)
	delete(settings, "context_policy")
	run.State["effective_config"] = config
	h.Settings.ContextPolicy = ContextPolicy{.8, .6}
	c, err := h.effectiveRunConfig(run)
	if err != nil || c.Settings.ContextPolicy != (ContextPolicy{}) {
		t.Fatalf("%+v %v", c, err)
	}
}
