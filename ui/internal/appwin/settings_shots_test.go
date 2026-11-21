package appwin

import (
	"image"
	"testing"

	"gioui.org/gpu/headless"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	shotSettings       = "settings"
	shotSettingsStates = "settings-states"
	testSettingsInc    = "inc-settings-shot"
	testSettingsWSID   = "ws-1"
	testApprovalAuto   = "auto"
	testThemeLight     = "light"
	testThemeDark      = "dark"
)

func buildShotMatrix(scenes ...string) []shotSpec {
	themes := []struct {
		name  string
		light bool
	}{{testThemeLight, true}, {testThemeDark, false}}
	scales := []struct {
		suffix string
		scale  float32
	}{{"ui090", 0.9}, {"ui100", 1.0}, {"ui125", 1.25}}
	specs := make([]shotSpec, 0, len(scenes)*len(themes)*len(scales))
	for _, s := range scenes {
		for _, sc := range scales {
			for _, th := range themes {
				specs = append(specs, shotSpec{
					name:    s + "-" + th.name + "-" + sc.suffix,
					scene:   s,
					light:   th.light,
					uiScale: sc.scale,
				})
			}
		}
	}
	return specs
}

func settingsShotSpecs() []shotSpec {
	return buildShotMatrix(shotSettings, shotSettingsStates)
}

func buildSettingsShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testSettingsInc})
	sc.workspaces.ApplyList([]workspace.Summary{
		{ID: testSettingsWSID, Name: "fugue", ConfigRevision: 3},
	})
	ws := client.Workspace{
		ID: testSettingsWSID, Name: "fugue", ConfigHash: "hash-shot-1", ConfigRevision: 3,
		RawTOML: []byte("name = \"fugue\"\n"),
	}
	eff := client.EffectiveWorkspaceConfig{
		Provenance: map[string]int64{
			settings.FieldApprovalMode:          client.ConfigTierWorkspace,
			settings.FieldSandboxProfile:        client.ConfigTierDefault,
			settings.FieldShellAllow:            client.ConfigTierWorkspace,
			settings.FieldIdleDetachSeconds:     client.ConfigTierGlobal,
			settings.FieldTaskDeadlineSeconds:   client.ConfigTierDefault,
			settings.FieldTaskMaxTurns:          client.ConfigTierDefault,
			settings.FieldSilenceTimeoutSeconds: client.ConfigTierDefault,
			settings.FieldTeamDeadlineSeconds:   client.ConfigTierDefault,
			settings.FieldTeamMaxAgents:         client.ConfigTierWorkspace,
		},
		Settings: client.WorkspaceSettings{
			ApprovalMode:          testApprovalAuto,
			SandboxProfile:        "workspace-write",
			ShellAllow:            []string{"git status", "go test"},
			EnvTags:               []client.EnvTagRule{{Match: "*.prod", Tag: "prod"}},
			SessionTeams:          true,
			TeamsAutoApprove:      false,
			IdleDetachSeconds:     180,
			TaskDeadlineSeconds:   3600,
			TaskMaxTurns:          50,
			SilenceTimeoutSeconds: 600,
			TeamDeadlineSeconds:   14400,
			TeamMaxAgents:         8,
		},
	}
	sc.settings.Prefs.Loaded = true
	sc.settings.Prefs.Current = settings.Preferences{ThemeMode: settings.ThemeLight, UIScale: settings.Scale100}
	sc.settings.SelectWorkspace(ws.ID, ws.Name)
	sc.settings.ApplyWorkspaceLoaded(&ws, &eff)
	if scene == shotSettingsStates {
		sc.settings.Draft.IdleDetachSeconds = 120
		sc.settings.Draft.ApprovalMode = testApprovalAsk
		sc.settings.PolicyConfirmArmed = true
		sc.settings.ChangedUnderneath = []settings.FieldChange{
			{ID: settings.FieldTeamMaxAgents, Label: settings.FieldLabel(settings.FieldTeamMaxAgents), OldValue: "12", NewValue: "8"},
		}
	}
	sc.settingsView.syncFromModel(sc.settings)
	sc.showPane(paneSettings)
	return sc
}

func renderSettingsShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	env := newAccountsTestEnv(t, spec.light, spec.uiScale)
	sc := buildSettingsShotScreens(t, spec.scene)
	if spec.light {
		sc.settings.Prefs.Current.ThemeMode = settings.ThemeLight
	} else {
		sc.settings.Prefs.Current.ThemeMode = settings.ThemeDark
	}
	sc.settings.Prefs.Current.UIScale = spec.uiScale
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), spec.name)
	if sc.lastErr != "" {
		t.Fatalf("settings shot %s recorded error: %s", spec.name, sc.lastErr)
	}
	return img
}

// TestSettingsShotsRender renders the F10.5d1 settings screens (light and dark
// at 90/100/125%) headlessly and checks that themes and scales produce distinct
// non-uniform images.
func TestSettingsShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range settingsShotSpecs() {
		img := renderSettingsShot(t, win, spec)
		if isUniformRGBA(img) {
			t.Fatalf("settings shot %s rendered uniform/blank image", spec.name)
		}
		rendered[spec.name] = img
	}
	for _, scene := range []string{shotSettings, shotSettingsStates} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
		if !imagesDifferRGBA(rendered[scene+"-light-ui090"], rendered[scene+"-light-ui100"]) ||
			!imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-light-ui125"]) {
			t.Fatalf("%s 90%%/100%%/125%% shots must differ", scene)
		}
	}
}
