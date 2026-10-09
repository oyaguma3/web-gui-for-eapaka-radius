package web

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
)

// eapaka-node-provisioner 経由のときに画面に出す名前。

// issueLabels は、2 つのノードの状態の食い違い（provisioner の加入者の issues）の名前。
var issueLabels = map[pvapi.Issue]string{
	pvapi.IssueKeyMissing:            "鍵がありません",
	pvapi.IssuePolicyMissing:         "認可ポリシーがありません",
	pvapi.IssueKeyInOtherStore:       "置き場所でない方にも鍵があります",
	pvapi.IssueAVClientNotAllowed:    "vector-gateway の AVクライアントを許可していません",
	pvapi.IssueOtherStoreUnreachable: "置き場所でない方を確かめられませんでした",
}

// issueHelps は食い違いの説明と対処。
var issueHelps = map[pvapi.Issue]string{
	pvapi.IssueKeyMissing: "置き場所に加入者（Ki / OPc）がないため、認証できません。" +
		"認可ポリシーだけが残っている場合は、加入者を削除してから登録し直してください。",
	pvapi.IssuePolicyMissing: "認可ポリシーがないため、本PoCは認証を拒否します。認可ポリシーを作成してください。",
	pvapi.IssueKeyInOtherStore: "PLMN マップで決まる置き場所でない方にも、同じ IMSI の加入者があります。" +
		"本PoCと provisioner の PLMN マップが一致しているか、Admin TUI などで登録していないかを確かめ、要らない方を削除してください。",
	pvapi.IssueAVClientNotAllowed: "aka-only-server の加入者が、本PoCの vector-gateway の AVクライアントを許可していないため、認証できません。" +
		"aka-only-server の管理 GUI などで、許可するクライアントに加えてください。",
	pvapi.IssueOtherStoreUnreachable: "provisioner が置き場所でない方の下流に接続できず、そちらに同じ IMSI の加入者がないかを確かめられませんでした。" +
		"ダッシュボードで接続の状態を確かめてください。",
}

func issueLabel(i pvapi.Issue) string { return cmp.Or(issueLabels[i], string(i)) }
func issueHelp(i pvapi.Issue) string  { return issueHelps[i] }

// keyStoreLabel は鍵の置き場所の名前。
func keyStoreLabel(ks pvapi.KeyStore) string {
	switch ks {
	case pvapi.KeyStorePoC:
		return "本PoC"
	case pvapi.KeyStoreAKA:
		return "aka-only-server"
	}
	return string(ks)
}

// opStatusLabels は操作の記録の状態の名前。
var opStatusLabels = map[string]string{
	pvapi.OpRunning:    "実行中",
	pvapi.OpCompleted:  "完了",
	pvapi.OpRolledBack: "元に戻した",
	pvapi.OpRetrying:   "やり直し中",
	pvapi.OpFailed:     "失敗（手での対応が必要）",
	pvapi.OpDismissed:  "閉じた",
}

func opStatusLabel(s string) string { return cmp.Or(opStatusLabels[s], s) }

// opKindLabel は操作の記録の種類の名前。
func opKindLabel(k string) string {
	return cmp.Or(map[string]string{
		"subscriber.create": "加入者の登録",
		"subscriber.update": "加入者の変更",
		"subscriber.delete": "加入者の削除",
	}[k], k)
}

// stepLabel は操作の記録の手順の名前。
func stepLabel(name string) string {
	return cmp.Or(map[string]string{
		"subscriber.create":     "加入者（鍵）の作成",
		"subscriber.update":     "加入者（鍵）の変更",
		"subscriber.delete":     "加入者（鍵）の削除",
		"subscriber.compensate": "作った加入者（鍵）の削除（元に戻す）",
		"policy.put":            "認可ポリシーの保存",
		"policy.delete":         "認可ポリシーの削除",
		"policy.restore":        "認可ポリシーを変更前に戻す",
	}[name], name)
}

// stepStateLabel は操作の記録の手順の状態の名前。
func stepStateLabel(state string) string {
	return cmp.Or(map[string]string{
		"pending":     "未実施",
		"done":        "済み",
		"failed":      "失敗",
		"compensated": "元に戻した",
		"skipped":     "不要",
	}[state], state)
}

// detailText は監査ログの内容（JSON のオブジェクト）を、項目の名前の順に「名前: 値」で並べた 1 行にする。
// aka-only-server の変更の { "from": …, "to": … } は「変更前 → 変更後」にする。
func detailText(m map[string]any) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		v := m[k]
		if fromTo, ok := v.(map[string]any); ok && len(fromTo) == 2 && fromTo["from"] != nil && fromTo["to"] != nil {
			parts = append(parts, fmt.Sprintf("%s: %v → %v", k, jsonValue(fromTo["from"]), jsonValue(fromTo["to"])))
			continue
		}
		parts = append(parts, k+": "+jsonValue(v))
	}
	return strings.Join(parts, "、")
}

// jsonValue は値を表示用の文字列にする（文字列はそのまま、それ以外は JSON）。
func jsonValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
