package gui

import (
	"remoteops/internal/i18n"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"time"
)

const (
	idPath = 101 + iota
	idBrowse
	idLoad
	idScope
	idConsent
	idKey
	idConnect
	idDisconnect
	idExit
	idTasks
	idHistory
	idLogsToggle
	idLogs
	idCancel
	idNewPairing
	idAuthorization
	idPathLabel
	idScopeLabel
	idKeyLabel
	idLanguage
	idLanguageLabel
	idMessages
)

type rect struct{ X, Y, W, H int }

func (a rect) overlaps(b rect) bool {
	return a.X < b.X+b.W && a.X+a.W > b.X && a.Y < b.Y+b.H && a.Y+a.H > b.Y
}
func scale(v, dpi int) int {
	if dpi <= 0 {
		dpi = 96
	}
	return (v*dpi + 48) / 96
}

// makeLayout uses logical pixels; callers enforce a 720×560 logical outer window.
func makeLayout(width, height, dpi int, connected, logs bool) map[int]rect {
	w, h := width*96/dpi, height*96/dpi
	m := map[int]rect{idExit: {w - 102, h - 43, 78, 30}, idLanguageLabel: {w - 278, 92, 104, 23}, idLanguage: {w - 166, 90, 134, 24}}
	if !connected {
		m[idMessages] = rect{32, h - 77, w - 64, 30}
		m[idPathLabel] = rect{32, 115, w - 64, 23}
		m[idScopeLabel] = rect{32, 174, w - 64, 24}
		m[idKeyLabel] = rect{32, h - 111, 92, 30}
		m[idPath] = rect{32, 140, w - 320, 30}
		m[idBrowse] = rect{w - 280, 140, 116, 30}
		m[idLoad] = rect{w - 156, 140, 124, 30}
		m[idScope] = rect{32, 202, w - 64, h - 382}
		m[idConsent] = rect{32, h - 169, w - 64, 48}
		m[idKey] = rect{132, h - 111, w - 350, 30}
		m[idConnect] = rect{w - 206, h - 111, 174, 32}
	} else {
		m[idMessages] = rect{32, h - 90, w - 64, 42}
		m[idCancel] = rect{w - 188, 248, 156, 30}
		m[idHistory] = rect{32, 318, 150, 28}
		m[idLogsToggle] = rect{192, 318, 150, 28}
		m[idAuthorization] = rect{352, 318, 150, 28}
		listW := w - 64
		if logs {
			listW = (w - 76) / 2
			m[idLogs] = rect{44 + listW, 354, w - 76 - listW, h - 448}
		}
		m[idTasks] = rect{32, 354, listW, h - 448}
		m[idDisconnect] = rect{w - 262, h - 43, 150, 30}
		m[idNewPairing] = rect{32, h - 43, 156, 30}
	}
	for k, r := range m {
		m[k] = rect{scale(r.X, dpi), scale(r.Y, dpi), scale(r.W, dpi), scale(r.H, dpi)}
	}
	return m
}
func canConnect(s client.Snapshot, consent, busy bool, now time.Time) bool {
	return !busy && consent && s.State == "ready" && !s.PairingExpiresAt.IsZero() && now.Before(s.PairingExpiresAt)
}
func selectedTask(tasks []worker.LocalTaskView, id string) (worker.LocalTaskView, bool) {
	for _, v := range tasks {
		if v.Task.ID == id {
			return v, true
		}
	}
	return worker.LocalTaskView{}, false
}
func taskStatus(v worker.LocalTaskView, language ...i18n.Language) string {
	return client.TaskResultText(v, language...)
}
