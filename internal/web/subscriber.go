package web

import (
	"net/http"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// subscribersPerPage は加入者の一覧の 1 ページの件数。
const subscribersPerPage = 50

// ---- 一覧 ----

type subscriberListData struct {
	Prefix  string
	Items   []provapi.Subscriber
	Total   int64
	Cursor  string // このページの cursor（先頭なら空）
	Next    string // 次のページの cursor
	Message string
	// DeletedWithPolicy は、削除した加入者と同じ IMSI の認可ポリシーが残っている場合の IMSI。
	DeletedWithPolicy string
	Error             string
}

func (h *Handler) subscribers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := subscriberListData{Prefix: q.Get("prefix"), Cursor: q.Get("cursor")}
	if v := q.Get("deleted"); imsiPattern.MatchString(v) {
		d.Message = "加入者 " + v + " を削除しました。"
		// 加入者を削除しても、同じ IMSI の認可ポリシーは残る（設計概要 §6）。
		if _, err := h.prov.GetPolicy(r.Context(), v); err == nil {
			d.DeletedWithPolicy = v
		}
	}
	if d.Prefix != "" && !prefixDigits.MatchString(d.Prefix) {
		d.Error = "IMSI の前方一致は 15 桁までの数字で指定してください。"
		h.render(w, r, http.StatusBadRequest, "subscribers", "加入者", d)
		return
	}
	list, err := h.prov.ListSubscribers(r.Context(), provapi.ListParams{
		Prefix: d.Prefix, Cursor: d.Cursor, Limit: subscribersPerPage,
	})
	if err != nil {
		status, msg := apiErrorMessage(err, "")
		h.log.Warn("list subscribers", "error", err)
		d.Error = msg
		h.render(w, r, status, "subscribers", "加入者", d)
		return
	}
	d.Items, d.Total, d.Next = list.Items, list.Total, list.NextCursor
	h.render(w, r, http.StatusOK, "subscribers", "加入者", d)
}

// ---- 登録 ----

type subscriberForm struct {
	IMSI, Ki, OPc, SQN, AMF string
	Errors                  fieldErrors
	Error                   string
}

func (h *Handler) subscriberNew(w http.ResponseWriter, r *http.Request) {
	f := subscriberForm{SQN: "000000000000", AMF: "8000", IMSI: r.URL.Query().Get("imsi")}
	h.render(w, r, http.StatusOK, "subscriber_new", "加入者の登録", f)
}

// subscriberCreate は加入者を登録する。全員が使える（一般ユーザーが Ki / OPc を扱えるのは、この入力中だけ）。
func (h *Handler) subscriberCreate(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	pf := r.PostForm
	f := subscriberForm{
		IMSI: strings.TrimSpace(pf.Get("imsi")), Ki: normalizeHex(pf.Get("ki")), OPc: normalizeHex(pf.Get("opc")),
		SQN: normalizeHex(pf.Get("sqn")), AMF: normalizeHex(pf.Get("amf")), Errors: fieldErrors{},
	}
	f.Errors.check(imsiPattern.MatchString(f.IMSI), "imsi", "IMSI は 15 桁の数字で入力してください。")
	f.Errors.check(hex128.MatchString(f.Ki), "ki", "Ki は 16 進 32 桁で入力してください。")
	f.Errors.check(hex128.MatchString(f.OPc), "opc", "OPc は 16 進 32 桁で入力してください。")
	f.Errors.check(sqnPattern.MatchString(f.SQN), "sqn", "SQN は 16 進 12 桁で入力してください。")
	f.Errors.check(amfPattern.MatchString(f.AMF), "amf", "AMF は 16 進 4 桁で入力してください。")
	if len(f.Errors) > 0 {
		f.Error = "入力を確かめてください。"
		h.render(w, r, http.StatusBadRequest, "subscriber_new", "加入者の登録", f)
		return
	}
	_, err := h.prov.CreateSubscriber(r.Context(), provapi.SubscriberCreate{
		IMSI: f.IMSI, Ki: f.Ki, OPc: f.OPc, SQN: f.SQN, AMF: f.AMF,
	})
	if err != nil {
		status, msg := apiErrorMessage(err, "")
		h.log.Warn("create subscriber", "error", err)
		f.Error = msg
		h.render(w, r, status, "subscriber_new", "加入者の登録", f)
		return
	}
	h.record(r, auditSubscriberCreate, f.IMSI, map[string]any{"amf": f.AMF, "sqn": f.SQN})
	http.Redirect(w, r, "/subscribers/"+f.IMSI+"?created=1", http.StatusSeeOther)
}

// ---- 詳細と変更 ----

type subscriberData struct {
	Sub provapi.Subscriber
	// Policy は同じ IMSI の認可ポリシー。なければ nil。
	Policy *provapi.Policy
	// PolicyError は認可ポリシーの有無を確かめられなかった場合の説明。
	PolicyError string
	// CanEditAuth は Ki / OPc の表示と、Ki / OPc / SQN / AMF の変更ができるか（管理者のみ）。
	CanEditAuth bool
	Message     string
	Error       string
	Errors      fieldErrors
}

// loadSubscriber は詳細の画面に出す値を集める。取得できなければ、書き出す応答のステータスと説明を返す。
func (h *Handler) loadSubscriber(r *http.Request, imsi string) (subscriberData, int, string) {
	me, _ := accountFrom(r.Context())
	d := subscriberData{CanEditAuth: me.IsAdmin(), Errors: fieldErrors{}}
	sub, err := h.prov.GetSubscriber(r.Context(), imsi)
	if err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("加入者 "+imsi))
		if status != http.StatusNotFound {
			h.log.Warn("get subscriber", "error", err)
		}
		return d, status, msg
	}
	d.Sub = sub
	switch p, err := h.prov.GetPolicy(r.Context(), imsi); {
	case err == nil:
		d.Policy = &p
	case provapi.CauseOf(err) != provapi.CausePolicyNotFound:
		h.log.Warn("get policy", "error", err)
		_, d.PolicyError = apiErrorMessage(err, "")
	}
	return d, http.StatusOK, ""
}

// subscriberIMSI は URL の IMSI を返す。形式が違えば 404 を返して false。
func (h *Handler) subscriberIMSI(w http.ResponseWriter, r *http.Request) (string, bool) {
	imsi := r.PathValue("imsi")
	if !imsiPattern.MatchString(imsi) {
		h.notFound(w, r)
		return "", false
	}
	return imsi, true
}

func (h *Handler) subscriber(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	d, status, msg := h.loadSubscriber(r, imsi)
	if status != http.StatusOK {
		h.renderErrorLink(w, r, status, msg, "/subscribers", "加入者の一覧へ")
		return
	}
	if r.URL.Query().Get("created") == "1" {
		d.Message = "加入者を登録しました。"
	}
	h.render(w, r, http.StatusOK, "subscriber", "加入者 "+imsi, d)
}

// renderSubscriber は変更の結果を詳細の画面（htmx では詳細の部分だけ）で返す。
func (h *Handler) renderSubscriber(w http.ResponseWriter, r *http.Request, imsi string, status int, mutate func(*subscriberData)) {
	d, loadStatus, msg := h.loadSubscriber(r, imsi)
	if loadStatus != http.StatusOK {
		h.renderErrorLink(w, r, loadStatus, msg, "/subscribers", "加入者の一覧へ")
		return
	}
	mutate(&d)
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "subscriber", "subscriber-detail", d)
		return
	}
	h.render(w, r, status, "subscriber", "加入者 "+imsi, d)
}

// subscriberAuth は Ki / OPc / SQN / AMF を変更する。管理者だけが使える。
// 入力のあった項目（Ki / OPc）と、値が変わった項目（SQN / AMF）だけを送る。
func (h *Handler) subscriberAuth(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	cur, err := h.prov.GetSubscriber(r.Context(), imsi)
	if err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("加入者 "+imsi))
		h.renderErrorLink(w, r, status, msg, "/subscribers", "加入者の一覧へ")
		return
	}
	pf := r.PostForm
	ki, opc := normalizeHex(pf.Get("ki")), normalizeHex(pf.Get("opc"))
	sqn, amf := normalizeHex(pf.Get("sqn")), normalizeHex(pf.Get("amf"))
	errs := fieldErrors{}
	errs.check(ki == "" || hex128.MatchString(ki), "ki", "Ki は 16 進 32 桁で入力してください。")
	errs.check(opc == "" || hex128.MatchString(opc), "opc", "OPc は 16 進 32 桁で入力してください。")
	errs.check(sqnPattern.MatchString(sqn), "sqn", "SQN は 16 進 12 桁で入力してください。")
	errs.check(amfPattern.MatchString(amf), "amf", "AMF は 16 進 4 桁で入力してください。")
	if len(errs) > 0 {
		h.renderSubscriber(w, r, imsi, http.StatusBadRequest, func(d *subscriberData) {
			d.Errors = errs
			d.Error = "入力を確かめてください。"
		})
		return
	}

	var u provapi.SubscriberUpdate
	var changed, fields []string
	if ki != "" {
		u.Ki, changed, fields = &ki, append(changed, "Ki"), append(fields, "ki")
	}
	if opc != "" {
		u.OPc, changed, fields = &opc, append(changed, "OPc"), append(fields, "opc")
	}
	// SQN は変えたときだけ送る（送らなければ、認証で進んだ SQN に触れない）。
	if sqn != cur.SQN {
		u.SQN, changed, fields = &sqn, append(changed, "SQN"), append(fields, "sqn")
	}
	if amf != cur.AMF {
		u.AMF, changed, fields = &amf, append(changed, "AMF"), append(fields, "amf")
	}
	if len(changed) == 0 {
		h.renderSubscriber(w, r, imsi, http.StatusOK, func(d *subscriberData) { d.Message = "変更はありません。" })
		return
	}
	if _, err := h.prov.UpdateSubscriber(r.Context(), imsi, u); err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("加入者 "+imsi))
		h.log.Warn("update subscriber", "error", err)
		h.renderSubscriber(w, r, imsi, status, func(d *subscriberData) { d.Error = msg })
		return
	}
	h.record(r, auditSubscriberUpdate, imsi, map[string]any{"fields": fields})
	h.renderSubscriber(w, r, imsi, http.StatusOK, func(d *subscriberData) {
		d.Message = strings.Join(changed, "、") + " を変更しました。"
	})
}

// subscriberKeys は Ki と OPc を表示する。管理者だけが使える。取得は provisioning-api の監査ログに残る。
func (h *Handler) subscriberKeys(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	keys, err := h.prov.GetSubscriberKeys(r.Context(), imsi)
	if err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("加入者 "+imsi))
		h.log.Warn("get subscriber keys", "error", err)
		h.renderBlock(w, r, status, "subscriber", "subscriber-keys", keysData{IMSI: imsi, Error: msg})
		return
	}
	h.record(r, auditSubscriberKeysRead, imsi, nil)
	h.renderBlock(w, r, http.StatusOK, "subscriber", "subscriber-keys", keysData{IMSI: imsi, Keys: &keys})
}

type keysData struct {
	IMSI  string
	Keys  *provapi.SubscriberKeys
	Error string
}

// subscriberDelete は加入者を削除する。全員が使える。同じ IMSI の認可ポリシーは残る。
func (h *Handler) subscriberDelete(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	if err := h.prov.DeleteSubscriber(r.Context(), imsi); err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("加入者 "+imsi))
		h.log.Warn("delete subscriber", "error", err)
		if status == http.StatusNotFound {
			// 既にないので、詳細を出し直せない。
			h.renderErrorLink(w, r, status, msg, "/subscribers", "加入者の一覧へ")
			return
		}
		h.renderSubscriber(w, r, imsi, status, func(d *subscriberData) { d.Error = msg })
		return
	}
	h.record(r, auditSubscriberDelete, imsi, nil)
	seeOther(w, r, "/subscribers?deleted="+imsi)
}
