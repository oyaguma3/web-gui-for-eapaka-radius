package web

import (
	"cmp"
	"crypto/rand"
	"net/http"
	"regexp"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
)

// eapaka-node-provisioner 経由のときの加入者の画面（設計概要 §12）。
// 加入者は「IMSI＋鍵の置き場所（本PoC / aka-only-server）＋認可ポリシー」として扱い、
// 登録では認可ポリシーも作り、削除では認可ポリシーも消す（provisioner が 2 つのノードの操作をまとめる）。

// idemKeyPattern は、フォームに持たせる Idempotency-Key の形式（crypto/rand.Text の出力）。
var idemKeyPattern = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// newIdemKey は、フォームを描くたびに作る Idempotency-Key を返す。
// 同じフォームを送り直しても（応答を待たずに押し直した、タイムアウトの後に送り直した）、provisioner は最初の結果を返す。
func newIdemKey() string { return rand.Text() }

// formIdemKey は、送られてきたフォームの Idempotency-Key を返す（形式が違えば空。空なら付けずに送る）。
func formIdemKey(r *http.Request) string {
	if k := r.PostForm.Get("idem"); idemKeyPattern.MatchString(k) {
		return k
	}
	return ""
}

// keepIdemKey は、失敗の後にフォームを描き直すときの Idempotency-Key を返す。
// provisioner に届かなかった（接続できない・タイムアウト）場合は、同じキーで送り直せるよう残す
// （provisioner は 5xx を覚えないので、届いて失敗していても同じキーでやり直せる）。
// provisioner が 4xx を返した場合は、その応答を覚えているので、新しいキーにする（入力を直して送ると内容が変わるため）。
func keepIdemKey(err error, key string) string {
	if key != "" && provapi.IsUnavailable(err) {
		return key
	}
	return newIdemKey()
}

// ---- 一覧 ----

type pvSubscriberListData struct {
	Prefix  string
	Items   []pvapi.Subscriber
	Cursor  string
	Next    string
	Message string
	Error   string
}

func (h *Handler) pvSubscribers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := pvSubscriberListData{Prefix: q.Get("prefix"), Cursor: q.Get("cursor")}
	if v := q.Get("deleted"); imsiPattern.MatchString(v) {
		d.Message = "加入者 " + v + " を削除しました（認可ポリシーも削除しました）。"
	}
	if d.Prefix != "" && !prefixDigits.MatchString(d.Prefix) {
		d.Error = "IMSI の前方一致は 15 桁までの数字で指定してください。"
		h.render(w, r, http.StatusBadRequest, "subscribers_pv", "加入者", d)
		return
	}
	if d.Cursor != "" && !imsiPattern.MatchString(d.Cursor) {
		d.Error = "ページの位置の指定が正しくありません。"
		h.render(w, r, http.StatusBadRequest, "subscribers_pv", "加入者", d)
		return
	}
	list, err := h.pv.ListSubscribers(r.Context(), provapi.ListParams{Prefix: d.Prefix, Cursor: d.Cursor, Limit: subscribersPerPage})
	if err != nil {
		status, msg := h.apiErrorMessage(err, "")
		h.log.Warn("list subscribers", "error", err)
		d.Error = msg
		h.render(w, r, status, "subscribers_pv", "加入者", d)
		return
	}
	d.Items, d.Next = list.Items, list.NextCursor
	h.render(w, r, http.StatusOK, "subscribers_pv", "加入者", d)
}

// ---- 登録 ----

type pvSubscriberForm struct {
	IMSI, Ki, OPc, SQN, AMF string
	// Default は認可ポリシーの既定の動作（allow / deny）。ルールは登録後に認可ポリシーの画面で編集する。
	Default string
	// PLMNMap は provisioner の PLMN マップ（鍵の置き場所の決め方の説明に出す。取得できなければ nil）。
	PLMNMap []pvapi.PLMNEntry
	// PLMNKnown は PLMN マップを取得できたか。
	PLMNKnown bool
	IdemKey   string
	Errors    fieldErrors
	Error     string
	// ErrorOperation は provisioner の操作の記録の ID（あればリンクを出す）。
	ErrorOperation string
}

// withPLMNMap は、フォームに provisioner の PLMN マップ（写し）を入れる。
func (h *Handler) withPLMNMap(r *http.Request, f *pvSubscriberForm) {
	if st, ok := h.pvInfo.get(r.Context()); ok {
		f.PLMNMap, f.PLMNKnown = st.PLMNMap, true
	}
}

func (h *Handler) pvSubscriberNew(w http.ResponseWriter, r *http.Request) {
	f := pvSubscriberForm{SQN: "000000000000", AMF: "8000", IMSI: r.URL.Query().Get("imsi"),
		Default: provapi.PolicyDeny, IdemKey: newIdemKey(), Errors: fieldErrors{}}
	h.withPLMNMap(r, &f)
	h.render(w, r, http.StatusOK, "subscriber_new_pv", "加入者の登録", f)
}

// readPVSubscriberForm は登録のフォームを読む（確かめない）。
func readPVSubscriberForm(r *http.Request) pvSubscriberForm {
	pf := r.PostForm
	f := pvSubscriberForm{
		IMSI: strings.TrimSpace(pf.Get("imsi")), Ki: normalizeHex(pf.Get("ki")), OPc: normalizeHex(pf.Get("opc")),
		SQN: normalizeHex(pf.Get("sqn")), AMF: normalizeHex(pf.Get("amf")), Default: pf.Get("default"),
		IdemKey: formIdemKey(r), Errors: fieldErrors{},
	}
	if f.Default != provapi.PolicyAllow {
		f.Default = provapi.PolicyDeny
	}
	return f
}

// renderPVSubscriberForm は登録のフォームを返す（htmx ではフォームの部分だけ）。
func (h *Handler) renderPVSubscriberForm(w http.ResponseWriter, r *http.Request, status int, f pvSubscriberForm) {
	h.withPLMNMap(r, &f)
	if f.IdemKey == "" {
		f.IdemKey = newIdemKey()
	}
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "subscriber_new_pv", "subscriber-new-form", f)
		return
	}
	h.render(w, r, status, "subscriber_new_pv", "加入者の登録", f)
}

// pvSubscriberNewForm は、既定の動作を切り替えたときにフォームを描き直す（allow のときだけ確認ダイアログを付ける）。
// 入力はそのまま返し、provisioner には何も送らない。
func (h *Handler) pvSubscriberNewForm(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	h.renderPVSubscriberForm(w, r, http.StatusOK, readPVSubscriberForm(r))
}

// pvSubscriberCreate は加入者を登録する（置き場所に鍵を作り、認可ポリシーを作る）。全員が使える。
func (h *Handler) pvSubscriberCreate(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	f := readPVSubscriberForm(r)
	f.Errors.check(imsiPattern.MatchString(f.IMSI), "imsi", "IMSI は 15 桁の数字で入力してください。")
	f.Errors.check(hex128.MatchString(f.Ki), "ki", "Ki は 16 進 32 桁で入力してください。")
	f.Errors.check(hex128.MatchString(f.OPc), "opc", "OPc は 16 進 32 桁で入力してください。")
	f.Errors.check(sqnPattern.MatchString(f.SQN), "sqn", "SQN は 16 進 12 桁で入力してください。")
	f.Errors.check(amfPattern.MatchString(f.AMF), "amf", "AMF は 16 進 4 桁で入力してください。")
	if len(f.Errors) > 0 {
		f.Error = "入力を確かめてください。"
		h.renderPVSubscriberForm(w, r, http.StatusBadRequest, f)
		return
	}
	sub, err := h.pv.CreateSubscriber(r.Context(), pvapi.SubscriberCreate{
		IMSI: f.IMSI, Ki: f.Ki, OPc: f.OPc, SQN: f.SQN, AMF: f.AMF,
		Policy: provapi.PolicyPut{Default: f.Default, Rules: []provapi.PolicyRule{}},
	}, f.IdemKey)
	if err != nil {
		fail := h.apiError(err, "")
		h.log.Warn("create subscriber", "error", err)
		f.Error, f.ErrorOperation, f.IdemKey = fail.Message, fail.OperationID, keepIdemKey(err, f.IdemKey)
		h.renderPVSubscriberForm(w, r, fail.Status, f)
		return
	}
	h.record(r, auditSubscriberCreate, f.IMSI, map[string]any{
		"keyStore": string(sub.KeyStore), "amf": f.AMF, "sqn": f.SQN, "policyDefault": f.Default})
	seeOther(w, r, "/subscribers/"+f.IMSI+"?created=1")
}

// ---- 詳細と変更 ----

type pvSubscriberData struct {
	Sub pvapi.Subscriber
	// CanEditAuth は Ki / OPc の表示と、鍵の属性の変更ができるか（管理者のみ）。
	CanEditAuth bool
	// AuthKey と DeleteKey は、変更と削除のフォームの Idempotency-Key。
	AuthKey, DeleteKey string
	Message            string
	Error              string
	ErrorOperation     string
	Errors             fieldErrors
}

// loadPVSubscriber は詳細の画面に出す値を集める。取得できなければ失敗を返す。
func (h *Handler) loadPVSubscriber(r *http.Request, imsi string) (pvSubscriberData, *apiFailure) {
	me, _ := accountFrom(r.Context())
	d := pvSubscriberData{CanEditAuth: me.IsAdmin(), AuthKey: newIdemKey(), DeleteKey: newIdemKey(), Errors: fieldErrors{}}
	sub, err := h.pv.GetSubscriber(r.Context(), imsi)
	if err != nil {
		f := h.apiError(err, notFoundMessage("加入者 "+imsi))
		if f.Status != http.StatusNotFound {
			h.log.Warn("get subscriber", "error", err)
		}
		return d, &f
	}
	d.Sub = sub
	return d, nil
}

func (h *Handler) pvSubscriber(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	d, fail := h.loadPVSubscriber(r, imsi)
	if fail != nil {
		h.renderFailure(w, r, *fail, "/subscribers", "加入者の一覧へ")
		return
	}
	if r.URL.Query().Get("created") == "1" {
		d.Message = "加入者を登録しました（認可ポリシーも作りました）。"
	}
	h.render(w, r, http.StatusOK, "subscriber_pv", "加入者 "+imsi, d)
}

// renderPVSubscriber は変更の結果を詳細の画面（htmx では詳細の部分だけ）で返す。
func (h *Handler) renderPVSubscriber(w http.ResponseWriter, r *http.Request, imsi string, status int, mutate func(*pvSubscriberData)) {
	h.renderPVSubscriberAfter(w, r, imsi, status, nil, mutate)
}

// renderPVSubscriberAfter は renderPVSubscriber と同じ。ただし、操作の失敗（opFail）の後に加入者を読み直せなかった場合は、
// 読み直しの失敗ではなく操作の失敗をエラー画面で返す（削除が途中で失敗した後など。操作の記録へのリンクを残す）。
func (h *Handler) renderPVSubscriberAfter(w http.ResponseWriter, r *http.Request, imsi string, status int, opFail *apiFailure,
	mutate func(*pvSubscriberData)) {
	d, fail := h.loadPVSubscriber(r, imsi)
	if fail != nil {
		h.renderFailure(w, r, *cmp.Or(opFail, fail), "/subscribers", "加入者の一覧へ")
		return
	}
	mutate(&d)
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "subscriber_pv", "subscriber-pv-detail", d)
		return
	}
	h.render(w, r, status, "subscriber_pv", "加入者 "+imsi, d)
}

// pvSubscriberAuth は鍵の属性（Ki / OPc / SQN / AMF と、aka-only-server の加入者の SQN の増加タイプ・平文HTTP の許可）を
// 変更する。管理者だけが使える。入力のあった項目（Ki / OPc）と、値が変わった項目だけを送る。
func (h *Handler) pvSubscriberAuth(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	cur, err := h.pv.GetSubscriber(r.Context(), imsi)
	if err != nil {
		h.renderFailure(w, r, h.apiError(err, notFoundMessage("加入者 "+imsi)), "/subscribers", "加入者の一覧へ")
		return
	}
	if cur.Key == nil {
		h.renderPVSubscriber(w, r, imsi, http.StatusConflict, func(d *pvSubscriberData) {
			d.Error = "この加入者には鍵がないため、変更できません。加入者を削除してから登録し直してください。"
		})
		return
	}
	pf := r.PostForm
	idem := formIdemKey(r)
	ki, opc := normalizeHex(pf.Get("ki")), normalizeHex(pf.Get("opc"))
	sqn, amf := normalizeHex(pf.Get("sqn")), normalizeHex(pf.Get("amf"))
	aka := cur.KeyStore == pvapi.KeyStoreAKA
	sqnType, allowPlain := pf.Get("sqn_type"), pf.Get("allow_plain") == "1"
	errs := fieldErrors{}
	errs.check(ki == "" || hex128.MatchString(ki), "ki", "Ki は 16 進 32 桁で入力してください。")
	errs.check(opc == "" || hex128.MatchString(opc), "opc", "OPc は 16 進 32 桁で入力してください。")
	errs.check(sqnPattern.MatchString(sqn), "sqn", "SQN は 16 進 12 桁で入力してください。")
	errs.check(amfPattern.MatchString(amf), "amf", "AMF は 16 進 4 桁で入力してください。")
	if aka {
		errs.check(sqnType == "inc1" || sqnType == "inc32" || sqnType == "inc33", "sqnType", "SQN の増加タイプを選んでください。")
	}
	if len(errs) > 0 {
		h.renderPVSubscriber(w, r, imsi, http.StatusBadRequest, func(d *pvSubscriberData) {
			d.Errors, d.Error, d.AuthKey = errs, "入力を確かめてください。", idem
		})
		return
	}

	var u pvapi.SubscriberUpdate
	var changed, fields []string
	if ki != "" {
		u.Ki, changed, fields = ki, append(changed, "Ki"), append(fields, "ki")
	}
	if opc != "" {
		u.OPc, changed, fields = opc, append(changed, "OPc"), append(fields, "opc")
	}
	// SQN は変えたときだけ送る（送らなければ、認証で進んだ SQN に触れない）。
	if sqn != cur.Key.SQN {
		u.SQN, changed, fields = sqn, append(changed, "SQN"), append(fields, "sqn")
	}
	if amf != cur.Key.AMF {
		u.AMF, changed, fields = amf, append(changed, "AMF"), append(fields, "amf")
	}
	if aka && sqnType != cur.Key.SQNType {
		u.SQNType, changed, fields = sqnType, append(changed, "SQN の増加タイプ"), append(fields, "sqnType")
	}
	if aka && (cur.Key.AllowPlain == nil || allowPlain != *cur.Key.AllowPlain) {
		u.AllowPlain, changed, fields = &allowPlain, append(changed, "平文HTTP の許可"), append(fields, "allowPlain")
	}
	if len(changed) == 0 {
		h.renderPVSubscriber(w, r, imsi, http.StatusOK, func(d *pvSubscriberData) { d.Message = "変更はありません。" })
		return
	}
	if _, err := h.pv.UpdateSubscriber(r.Context(), imsi, u, idem); err != nil {
		fail := h.apiError(err, notFoundMessage("加入者 "+imsi))
		h.log.Warn("update subscriber", "error", err)
		h.renderPVSubscriberAfter(w, r, imsi, fail.Status, &fail, func(d *pvSubscriberData) {
			d.Error, d.ErrorOperation, d.AuthKey = fail.Message, fail.OperationID, keepIdemKey(err, idem)
		})
		return
	}
	h.record(r, auditSubscriberUpdate, imsi, map[string]any{"keyStore": string(cur.KeyStore), "fields": fields})
	h.renderPVSubscriber(w, r, imsi, http.StatusOK, func(d *pvSubscriberData) {
		d.Message = strings.Join(changed, "、") + " を変更しました。"
	})
}

// pvSubscriberDelete は加入者を削除する（認可ポリシーと、置き場所の鍵を消す）。全員が使える。
func (h *Handler) pvSubscriberDelete(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	idem := formIdemKey(r)
	if err := h.pv.DeleteSubscriber(r.Context(), imsi, idem); err != nil {
		fail := h.apiError(err, notFoundMessage("加入者 "+imsi))
		h.log.Warn("delete subscriber", "error", err)
		if fail.Status == http.StatusNotFound {
			// 既にないので、詳細を出し直せない。
			h.renderFailure(w, r, fail, "/subscribers", "加入者の一覧へ")
			return
		}
		h.renderPVSubscriberAfter(w, r, imsi, fail.Status, &fail, func(d *pvSubscriberData) {
			d.Error, d.ErrorOperation, d.DeleteKey = fail.Message, fail.OperationID, keepIdemKey(err, idem)
		})
		return
	}
	h.record(r, auditSubscriberDelete, imsi, nil)
	seeOther(w, r, "/subscribers?deleted="+imsi)
}
