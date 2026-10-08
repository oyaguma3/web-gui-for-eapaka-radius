package web

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// RADIUSクライアントの入力の誤りの説明。
const (
	msgClientIP     = "IP アドレスは IPv4 のドット区切り（例 192.168.10.1。各数の先頭に 0 を付けない）で入力してください。"
	msgClientSecret = "共有シークレットは、空白を含まない英数字・記号（印字可能な ASCII）の 1〜128 文字で入力してください。"
	msgClientName   = "名前は英数字と _ - の 1〜64 文字で入力してください。"
	msgClientVendor = "ベンダーは英数字・空白・- の 64 文字までで入力してください。"
)

// clientForm は RADIUSクライアントの入力。
type clientForm struct {
	IP, Secret, Name, Vendor string
	Errors                   fieldErrors
}

// readClientForm はフォームの入力を読み、確かめる。requireSecret が false なら、共有シークレットは空でもよい（変更しない）。
func readClientForm(r *http.Request, requireSecret bool) clientForm {
	pf := r.PostForm
	f := clientForm{
		IP: strings.TrimSpace(pf.Get("ip")), Secret: strings.TrimSpace(pf.Get("secret")),
		Name: strings.TrimSpace(pf.Get("name")), Vendor: strings.TrimSpace(pf.Get("vendor")),
		Errors: fieldErrors{},
	}
	f.Errors.check(validIPv4(f.IP), "ip", msgClientIP)
	f.Errors.check((!requireSecret && f.Secret == "") || secretPattern.MatchString(f.Secret), "secret", msgClientSecret)
	f.Errors.check(clientNamePattern.MatchString(f.Name), "name", msgClientName)
	f.Errors.check(vendorPattern.MatchString(f.Vendor), "vendor", msgClientVendor)
	return f
}

// ---- 一覧と登録 ----

type clientsData struct {
	Clients []provapi.RADIUSClient
	CanEdit bool
	Message string
	Error   string
	Form    clientForm
}

func (h *Handler) clientsData(r *http.Request) (clientsData, int, string) {
	me, _ := accountFrom(r.Context())
	d := clientsData{CanEdit: me.IsAdmin(), Form: clientForm{Errors: fieldErrors{}}}
	clients, err := h.prov.ListRADIUSClients(r.Context())
	if err != nil {
		status, msg := apiErrorMessage(err, "")
		h.log.Warn("list radius clients", "error", err)
		return d, status, msg
	}
	d.Clients = clients
	return d, http.StatusOK, ""
}

func (h *Handler) clients(w http.ResponseWriter, r *http.Request) {
	d, status, msg := h.clientsData(r)
	d.Error = msg
	if v := r.URL.Query().Get("deleted"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			d.Message = "RADIUSクライアント #" + strconv.FormatInt(id, 10) + " を削除しました。"
		}
	}
	h.render(w, r, status, "clients", "RADIUSクライアント", d)
}

// clientCreate は RADIUSクライアントを登録する。管理者だけが使える。ID はサーバーが採番する。
func (h *Handler) clientCreate(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	f := readClientForm(r, true)
	fail := func(status int, msg string) {
		d, _, loadMsg := h.clientsData(r)
		d.Form, d.Error = f, cmp.Or(msg, loadMsg)
		h.renderClients(w, r, status, d)
	}
	if len(f.Errors) > 0 {
		fail(http.StatusBadRequest, "入力を確かめてください。")
		return
	}
	c, err := h.prov.CreateRADIUSClient(r.Context(), provapi.RADIUSClientCreate{
		IP: f.IP, Secret: f.Secret, Name: f.Name, Vendor: f.Vendor,
	})
	if err != nil {
		status, msg := apiErrorMessage(err, "")
		h.log.Warn("create radius client", "error", err)
		fail(status, msg)
		return
	}
	h.record(r, auditClientCreate, clientTarget(c.ID), map[string]any{"ip": c.IP, "name": c.Name, "vendor": c.Vendor})
	d, status, msg := h.clientsData(r)
	d.Error = msg
	d.Message = "RADIUSクライアント " + c.Name + "（#" + strconv.FormatInt(c.ID, 10) + "、" + c.IP + "）を登録しました。"
	h.renderClients(w, r, status, d)
}

// clientTarget は監査ログの対象に書く RADIUSクライアントの表記。
func clientTarget(id int64) string { return "#" + strconv.FormatInt(id, 10) }

// renderClients は一覧の画面（htmx では一覧と登録フォームの部分だけ）を返す。
func (h *Handler) renderClients(w http.ResponseWriter, r *http.Request, status int, d clientsData) {
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "clients", "clients-section", d)
		return
	}
	h.render(w, r, status, "clients", "RADIUSクライアント", d)
}

// ---- 詳細と変更 ----

type clientData struct {
	Client  provapi.RADIUSClient
	CanEdit bool
	Message string
	Error   string
	// Form は変更の入力（誤りのときに戻す。共有シークレットは戻さない）。
	Form clientForm
}

// clientID は URL の RADIUSクライアントの ID を返す。形式が違えば 404 を返して false。
func (h *Handler) clientID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		h.notFound(w, r)
		return 0, false
	}
	return id, true
}

func (h *Handler) loadClient(r *http.Request, id int64) (clientData, int, string) {
	me, _ := accountFrom(r.Context())
	d := clientData{CanEdit: me.IsAdmin(), Form: clientForm{Errors: fieldErrors{}}}
	c, err := h.prov.GetRADIUSClient(r.Context(), id)
	if err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("RADIUSクライアント #"+strconv.FormatInt(id, 10)))
		if status != http.StatusNotFound {
			h.log.Warn("get radius client", "error", err)
		}
		return d, status, msg
	}
	d.Client = c
	d.Form.IP, d.Form.Name, d.Form.Vendor = c.IP, c.Name, c.Vendor
	return d, http.StatusOK, ""
}

func (h *Handler) client(w http.ResponseWriter, r *http.Request) {
	id, ok := h.clientID(w, r)
	if !ok {
		return
	}
	d, status, msg := h.loadClient(r, id)
	if status != http.StatusOK {
		h.renderErrorLink(w, r, status, msg, "/clients", "RADIUSクライアントの一覧へ")
		return
	}
	h.render(w, r, http.StatusOK, "client", "RADIUSクライアント "+d.Client.Name, d)
}

// renderClient は変更の結果を詳細の画面（htmx では詳細の部分だけ）で返す。
func (h *Handler) renderClient(w http.ResponseWriter, r *http.Request, id int64, status int, mutate func(*clientData)) {
	d, loadStatus, msg := h.loadClient(r, id)
	if loadStatus != http.StatusOK {
		h.renderErrorLink(w, r, loadStatus, msg, "/clients", "RADIUSクライアントの一覧へ")
		return
	}
	mutate(&d)
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "client", "client-detail", d)
		return
	}
	h.render(w, r, status, "client", "RADIUSクライアント "+d.Client.Name, d)
}

// clientUpdate は IP アドレス・名前・ベンダー・共有シークレットを変更する。管理者だけが使える。
// 値が変わった項目と、入力のあった共有シークレットだけを送る。IP を変えても ID は変わらない。
func (h *Handler) clientUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.clientID(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	f := readClientForm(r, false)
	if len(f.Errors) > 0 {
		h.renderClient(w, r, id, http.StatusBadRequest, func(d *clientData) {
			d.Form, d.Error = f, "入力を確かめてください。"
		})
		return
	}
	cur, err := h.prov.GetRADIUSClient(r.Context(), id)
	if err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("RADIUSクライアント #"+strconv.FormatInt(id, 10)))
		h.renderErrorLink(w, r, status, msg, "/clients", "RADIUSクライアントの一覧へ")
		return
	}
	var u provapi.RADIUSClientUpdate
	var changed, fields []string
	detail := map[string]any{}
	if f.IP != cur.IP {
		u.IP, changed, fields = &f.IP, append(changed, "IP アドレス"), append(fields, "ip")
		detail["ip"] = cur.IP + " -> " + f.IP
	}
	if f.Name != cur.Name {
		u.Name, changed, fields = &f.Name, append(changed, "名前"), append(fields, "name")
	}
	if f.Vendor != cur.Vendor {
		u.Vendor, changed, fields = &f.Vendor, append(changed, "ベンダー"), append(fields, "vendor")
	}
	if f.Secret != "" {
		u.Secret, changed, fields = &f.Secret, append(changed, "共有シークレット"), append(fields, "secret")
	}
	if len(changed) == 0 {
		h.renderClient(w, r, id, http.StatusOK, func(d *clientData) { d.Message = "変更はありません。" })
		return
	}
	if _, err := h.prov.UpdateRADIUSClient(r.Context(), id, u); err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("RADIUSクライアント #"+strconv.FormatInt(id, 10)))
		h.log.Warn("update radius client", "error", err)
		if status == http.StatusNotFound {
			h.renderErrorLink(w, r, status, msg, "/clients", "RADIUSクライアントの一覧へ")
			return
		}
		h.renderClient(w, r, id, status, func(d *clientData) {
			f.Secret = ""
			d.Form, d.Error = f, msg
		})
		return
	}
	detail["fields"] = fields
	h.record(r, auditClientUpdate, clientTarget(id), detail)
	h.renderClient(w, r, id, http.StatusOK, func(d *clientData) {
		d.Message = strings.Join(changed, "、") + " を変更しました。すぐに反映されます。"
	})
}

type secretData struct {
	ID     int64
	Secret string
	Shown  bool
	Error  string
}

// clientSecret は共有シークレットを表示する。管理者だけが使える。取得は provisioning-api の監査ログに残る。
func (h *Handler) clientSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := h.clientID(w, r)
	if !ok {
		return
	}
	secret, err := h.prov.GetRADIUSClientSecret(r.Context(), id)
	if err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("RADIUSクライアント #"+strconv.FormatInt(id, 10)))
		h.log.Warn("get radius client secret", "error", err)
		h.renderBlock(w, r, status, "client", "client-secret", secretData{ID: id, Error: msg})
		return
	}
	h.record(r, auditClientSecretRead, clientTarget(id), nil)
	h.renderBlock(w, r, http.StatusOK, "client", "client-secret", secretData{ID: id, Secret: secret, Shown: true})
}

// clientDelete は RADIUSクライアントを削除する。管理者だけが使える。
func (h *Handler) clientDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.clientID(w, r)
	if !ok {
		return
	}
	if err := h.prov.DeleteRADIUSClient(r.Context(), id); err != nil {
		status, msg := apiErrorMessage(err, notFoundMessage("RADIUSクライアント #"+strconv.FormatInt(id, 10)))
		h.log.Warn("delete radius client", "error", err)
		if status == http.StatusNotFound {
			h.renderErrorLink(w, r, status, msg, "/clients", "RADIUSクライアントの一覧へ")
			return
		}
		h.renderClient(w, r, id, status, func(d *clientData) { d.Error = msg })
		return
	}
	h.record(r, auditClientDelete, clientTarget(id), nil)
	seeOther(w, r, "/clients?deleted="+strconv.FormatInt(id, 10))
}
