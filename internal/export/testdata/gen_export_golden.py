"""Generate testdata/python_golden.json for internal/export by running the REAL
app.py export handlers under CPython 3.12.

app.py is imported from a scratchpad copy, so APP_DIR / STATE_FILE point at the
scratchpad and nothing under the real project is touched. No network is used:
the sub2api path is fed a hand-built refresh payload instead of a live refresh.
"""

import base64
import json
import os
import sys
import tempfile
import zipfile
from datetime import datetime as RealDT, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import app  # noqa: E402

# --------------------------------------------------------------------------
# Pinned clocks
# --------------------------------------------------------------------------
FIXED_UTC = RealDT(2026, 7, 26, 12, 34, 56, 789000, tzinfo=timezone.utc)
FIXED_LOCAL = FIXED_UTC.astimezone().replace(tzinfo=None)
FIXED_UNIX = 1785000000


class FakeDT(RealDT):
    @classmethod
    def now(cls, tz=None):
        return FIXED_UTC if tz is not None else FIXED_LOCAL


app.datetime = FakeDT
app.time.time = lambda: float(FIXED_UNIX)


# --------------------------------------------------------------------------
# Tk stubs
# --------------------------------------------------------------------------
class MessageBoxStub:
    def __init__(self):
        self.calls = []

    def showwarning(self, title, message, **kw):
        self.calls.append(("warning", message))

    def showinfo(self, title, message, **kw):
        self.calls.append(("info", message))

    def askyesno(self, title, message, **kw):
        self.calls.append(("yesno", message))
        return False


class FileDialogStub:
    def __init__(self):
        self.calls = []
        self.path = ""

    def asksaveasfilename(self, **kw):
        self.calls.append(kw)
        return self.path


MB = MessageBoxStub()
FD = FileDialogStub()
app.messagebox = MB
app.filedialog = FD

TMP = tempfile.mkdtemp(prefix="export-golden-")
_counter = [0]


def temp_path(ext):
    _counter[0] += 1
    return os.path.join(TMP, "out-%03d%s" % (_counter[0], ext))


class Var:
    def __init__(self, value):
        self._value = value

    def get(self):
        return self._value


class Stub:
    """A self with only what the export handlers touch."""

    def __init__(self, accounts, results, fmt="sub2api", prefix=""):
        self.accounts = list(accounts)
        self.selected = list(accounts)
        self.session_results = results
        self.export_name_prefix = Var(prefix)
        self.session_convert_format = Var(fmt)
        self.logs = []
        self.running = False
        self.dialog = None
        self.saved_path = ""

    # --- stubs for the impure collaborators -------------------------------
    def log(self, message):
        self.logs.append(message)

    def _emit_log(self, message):
        self.logs.append(message)

    def save_state(self):
        pass

    def _selected_accounts_for_export(self):
        return list(self.selected)

    def _start_cpa_rt_refresh_for_conversion(self, action, accounts=None):
        return False

    def _preview_and_save_text(self, title, text, default_extension=".txt", filetypes=None):
        self.dialog = {
            "title": title,
            "default_extension": default_extension,
            "filetypes": filetypes or [("Text", "*.txt"), ("All", "*.*")],
        }
        self.saved_path = temp_path(default_extension)
        return self.saved_path


for _name in (
    "_finish_export_authorized",
    "_finish_export_authorized_email_rt",
    "export_selected_sessions",
    "_selected_session_conversions",
    "_selected_session_conversion_text",
    "export_selected_session_conversion",
    "export_selected_session_conversion_zip",
    "export_selected_raw",
    "_ensure_export_accounts_have_rt",
):
    setattr(Stub, _name, getattr(app.App, _name))


# --------------------------------------------------------------------------
# Fixtures
# --------------------------------------------------------------------------
def b64url(raw):
    return base64.urlsafe_b64encode(json.dumps(raw, ensure_ascii=False).encode("utf-8")).rstrip(b"=").decode("ascii")


def jwt(payload):
    return "%s.%s.%s" % (b64url({"alg": "none", "typ": "JWT"}), b64url(payload), "sig")


AUTH = "https://api.openai.com/auth"
PROFILE = "https://api.openai.com/profile"

ACCESS = jwt({
    "sub": "user-alpha",
    "exp": 1786000000,
    "iat": 1785000000,
    "email": "claims@example.com",
    AUTH: {
        "chatgpt_account_id": "acct-alpha",
        "chatgpt_user_id": "cu-alpha",
        "chatgpt_plan_type": "plus",
        "organizations": [{"id": "org-alpha", "title": "A&B <Org>"}],
    },
    PROFILE: {"email": "profile@example.com"},
})
IDTOK = jwt({
    "sub": "user-alpha",
    "exp": 1786000000,
    "email": "id@example.com",
    AUTH: {"chatgpt_account_id": "acct-alpha", "chatgpt_plan_type": "plus"},
})
ACCESS2 = jwt({
    "sub": "user-beta",
    "exp": 1786500000,
    "iat": 1785100000,
    AUTH: {"chatgpt_account_id": "acct-beta", "chatgpt_user_id": "cu-beta", "chatgpt_plan_type": "team"},
})
IDTOK2 = jwt({"sub": "user-beta", AUTH: {"chatgpt_account_id": "acct-beta"}})

PREFIX = "标记&<T>"

ACCOUNT_SPECS = [
    {
        "email": "alpha@example.com", "password": "pw1", "client_id": "cid1",
        "refresh_token": "rft1", "raw": "", "openai_rt": "rt_alpha",
        "auth_phone_number": "+15550001", "mail_provider": "hotmail",
    },
    # & and <> in the email prove json.dumps does not HTML-escape.
    {
        "email": "beta&<b>@example.com", "password": "pw2", "client_id": "", "refresh_token": "",
        "raw": "", "openai_rt": "",
    },
    # Non-ASCII proves ensure_ascii=False; the raw line already carries rt_token.
    {
        "email": "甘马@例子.中国", "password": "pw3", "client_id": "cid3", "refresh_token": "rft3",
        "raw": "甘马@例子.中国----pw3----cid3----rft3----rt_token=rt_old", "openai_rt": "rt.gamma",
    },
    # No session at all -> missing / skipped.
    {"email": "delta@example.com", "password": "pw4", "openai_rt": "rt_delta"},
    # Same EmailKey as alpha -> forces the ZIP entry-name de-duplication.
    {"email": "Alpha@Example.com", "password": "pw5", "openai_rt": "rt_alpha2"},
]

SESSION_ALPHA = json.dumps({
    "accessToken": ACCESS,
    "user": {"email": "session@example.com", "id": "uid-alpha"},
    "expires": "2026-08-25T09:19:37.911Z",
    # U+2028 LINE SEPARATOR is deliberate: encoding/json escapes it and
    # json.dumps does not, so it pins the export package's un-escaping.
    "note": "a&b <c> 中文 tail",
}, ensure_ascii=False)

SESSION_BETA = json.dumps({"access_token": ACCESS2, "idToken": IDTOK2, "account": {"id": "acct-beta"}})

SESSION_RESULTS = {
    "alpha@example.com": {
        "session_json": SESSION_ALPHA,
        "access_token": ACCESS,
        "id_token": IDTOK,
        "openai_rt": "rt_alpha_payload",
        "plan_type": "plus",
        "account_id": "acct-alpha",
        "access_summary": {"plan_type": "team", "backend_plan_type": "pro"},
    },
    "beta&<b>@example.com": {"session_json": SESSION_BETA},
    # session_json is whitespace-only -> stripped to "" -> "missing" for the
    # Session export, but the payload's own access_token still converts.
    "甘马@例子.中国": {
        "session_json": "   ",
        "access_token": ACCESS2,
        "id_token": IDTOK2,
        "chatgpt_plan_type": "pro",
        "chatgpt_account_id": "acct-gamma",
    },
    "delta@example.com": {},
    "Alpha@Example.com": {"session_json": SESSION_ALPHA, "id_token": IDTOK},
}


ACCOUNT_FIELDS = {f.name for f in app.dataclasses.fields(app.MailAccount)}
REQUIRED = ("email", "password", "client_id", "refresh_token", "raw")


def make_accounts():
    out = []
    for spec in ACCOUNT_SPECS:
        kwargs = {key: "" for key in REQUIRED}
        kwargs.update(spec)
        assert set(kwargs) <= ACCOUNT_FIELDS, set(kwargs) - ACCOUNT_FIELDS
        out.append(app.MailAccount(**kwargs))
    return out


def mk(email, **kw):
    kwargs = {key: "" for key in REQUIRED}
    kwargs["email"] = email
    kwargs.update(kw)
    return app.MailAccount(**kwargs)


GOLDEN = {}


def read_bytes(path):
    with open(path, "rb") as handle:
        return base64.b64encode(handle.read()).decode("ascii")


def scrub(lines):
    """Replace the volatile temp path so the golden is byte-reproducible."""
    return [line.replace(TMP, "<TMP>") for line in lines]


def record(key, stub, text):
    GOLDEN[key] = json.dumps({
        "title": stub.dialog["title"],
        "default_extension": stub.dialog["default_extension"],
        "filetypes": stub.dialog["filetypes"],
        "text": text,
        "file_b64": read_bytes(stub.saved_path),
        "logs": scrub(stub.logs),
    }, ensure_ascii=False)


def captured_text(stub):
    with open(stub.saved_path, "rb") as handle:
        raw = handle.read()
    return raw.decode("utf-8")


# --------------------------------------------------------------------------
# 1. 导出选中Raw  (app.py:24406-24416)
# --------------------------------------------------------------------------
accounts = make_accounts()
stub = Stub(accounts, SESSION_RESULTS, prefix=PREFIX)
stub.export_selected_raw()
record("raw", stub, app.Path(stub.saved_path).read_text(encoding="utf-8"))

# --------------------------------------------------------------------------
# 2. 导出已授权邮箱  (app.py:24106-24116)
# --------------------------------------------------------------------------
accounts = make_accounts()
stub = Stub(accounts, SESSION_RESULTS, prefix=PREFIX)
stub._finish_export_authorized(list(accounts), PREFIX)
record("authorized", stub, app.Path(stub.saved_path).read_text(encoding="utf-8"))

# --------------------------------------------------------------------------
# 3. 导出邮箱----RT  (app.py:24126-24136)
# --------------------------------------------------------------------------
accounts = make_accounts()
stub = Stub(accounts, SESSION_RESULTS)
stub._finish_export_authorized_email_rt(list(accounts))
record("email_rt", stub, app.Path(stub.saved_path).read_text(encoding="utf-8"))

# --------------------------------------------------------------------------
# 4. 导出选中Session  (app.py:24138-24166)
# --------------------------------------------------------------------------
accounts = make_accounts()
stub = Stub(accounts, SESSION_RESULTS)
stub.export_selected_sessions()
record("sessions", stub, app.Path(stub.saved_path).read_text(encoding="utf-8"))

# --------------------------------------------------------------------------
# 5. 导出 Session 转换 <label>  (app.py:24355-24375), every format
# --------------------------------------------------------------------------
FORMATS = list(app.SESSION_CONVERT_FORMATS) + ["NONSENSE"]
for fmt in FORMATS:
    accounts = make_accounts()
    stub = Stub(accounts, SESSION_RESULTS, fmt=fmt)
    stub.export_selected_session_conversion()
    record("conv:" + fmt, stub, app.Path(stub.saved_path).read_text(encoding="utf-8"))

# --------------------------------------------------------------------------
# 6. 导出 Session 转换 ZIP  (app.py:24377-24404)
# --------------------------------------------------------------------------
for fmt in ("sub2api", "codexmanager"):
    accounts = make_accounts()
    stub = Stub(accounts, SESSION_RESULTS, fmt=fmt)
    FD.calls = []
    FD.path = temp_path(".zip")
    stub.export_selected_session_conversion_zip()
    entries = []
    with zipfile.ZipFile(FD.path) as archive:
        for name in archive.namelist():
            entries.append({"name": name, "data_b64": base64.b64encode(archive.read(name)).decode("ascii")})
    GOLDEN["zip:" + fmt] = json.dumps({
        "dialog": {k: v for k, v in FD.calls[-1].items()},
        "entries": entries,
        "logs": scrub(stub.logs),
    }, ensure_ascii=False)

# --------------------------------------------------------------------------
# 7. 导出 sub2api JSON  (app.py:24500-24533) — the refresh is replaced by a
#    hand-built token payload; everything downstream is the real code.
# --------------------------------------------------------------------------
accounts = [account for account in make_accounts() if account.openai_rt]
records = []
for account in accounts:
    token_payload = {"access_token": ACCESS, "id_token": IDTOK, "refresh_token": "rt_from_server"}
    refreshed_rt = str(token_payload.get("refresh_token") or "")
    if app.is_openai_refresh_token(refreshed_rt):
        account.openai_rt = refreshed_rt
    token_payload["refresh_token"] = account.openai_rt
    export_email = f"({PREFIX}){account.email}" if PREFIX else account.email
    records.append(app.openai_record_from_refresh_payload(export_email, token_payload))
sub2api_path = temp_path(".sub2api.json")
sub2api_text = json.dumps(app.build_sub2api_export(records), ensure_ascii=False, indent=2) + "\n"
app.Path(sub2api_path).write_text(sub2api_text, encoding="utf-8")
GOLDEN["sub2api"] = json.dumps({
    "records": records,
    "text": sub2api_text,
    "file_b64": read_bytes(sub2api_path),
}, ensure_ascii=False)

# --------------------------------------------------------------------------
# 8. Warning / prompt strings
# --------------------------------------------------------------------------
messages = {}

MB.calls = []
stub = Stub([], SESSION_RESULTS)
stub._finish_export_authorized([mk("x@y.z")], "")
messages["no_authorized_rt"] = MB.calls[-1][1]

MB.calls = []
stub = Stub([], SESSION_RESULTS)
stub._finish_export_authorized_email_rt([mk("x@y.z")])
messages["no_authorized_rt_email"] = MB.calls[-1][1]

MB.calls = []
no_session = [mk("delta@example.com", openai_rt="rt_delta")]
stub = Stub(no_session, SESSION_RESULTS)
stub.export_selected_sessions()
messages["no_session_json"] = MB.calls[-1][1]

MB.calls = []
stub = Stub(no_session, SESSION_RESULTS, fmt="cpa")
stub.export_selected_session_conversion()
messages["no_convertible_token"] = MB.calls[-1][1]

MB.calls = []
stub = Stub(no_session, SESSION_RESULTS, fmt="cpa")
FD.path = temp_path(".zip")
stub.export_selected_session_conversion_zip()
messages["no_convertible_token_zip"] = MB.calls[-1][1]

MB.calls = []
stub = Stub(make_accounts(), SESSION_RESULTS)
missing_many = [mk("m%02d@example.com" % i) for i in range(15)]
stub._ensure_export_accounts_have_rt(missing_many, "authorized")
messages["missing_rt_prompt_15"] = MB.calls[-1][1]

MB.calls = []
stub = Stub(make_accounts(), SESSION_RESULTS)
stub._ensure_export_accounts_have_rt(missing_many[:3], "authorized")
messages["missing_rt_prompt_3"] = MB.calls[-1][1]

# The empty-selection warning lives in _selected_accounts_for_export
# (app.py:24421); read the literal straight out of the source.
source = app.Path(app.__file__).read_text(encoding="utf-8").splitlines()
messages["no_selection"] = source[24420].split('"')[1]
messages["no_sub2api_records"] = source[24531].split('"')[1]
messages["no_authorized_selection"] = source[24435].split('"')[1]

GOLDEN["messages"] = json.dumps(messages, ensure_ascii=False)

# Skip-note shapes (app.py:24353 / 24375 / 24404).
skipped_many = ["s%d@example.com" % i for i in range(7)]
skipped_few = ["s0@example.com", "s1@example.com"]


def skip_note(prefix, skipped):
    return f"{prefix}跳过 {len(skipped)} 个: {', '.join(skipped[:5])}" + (
        f" 等 {len(skipped)} 个" if len(skipped) > 5 else ""
    )


GOLDEN["skip_notes"] = json.dumps({
    "many": skip_note("Session 转换", skipped_many),
    "few": skip_note("Session 转换", skipped_few),
    "zip_many": skip_note("Session 转换 ZIP", skipped_many),
}, ensure_ascii=False)

# --------------------------------------------------------------------------
# 9. The inputs, so the Go test builds byte-identical fixtures.
# --------------------------------------------------------------------------
GOLDEN["input"] = json.dumps({
    "prefix": PREFIX,
    "accounts": ACCOUNT_SPECS,
    "session_results": SESSION_RESULTS,
    "now": FIXED_UTC.isoformat().replace("+00:00", "Z"),
    "zip_stamp": FIXED_LOCAL.strftime("%Y%m%d-%H%M%S"),
    "access": ACCESS,
    "id_token": IDTOK,
    "sub2api_records": records,
}, ensure_ascii=False)

out = sys.argv[1]
os.makedirs(os.path.dirname(out), exist_ok=True)
with open(out, "w", encoding="utf-8", newline="\n") as handle:
    json.dump(GOLDEN, handle, ensure_ascii=False, indent=1, sort_keys=True)
    handle.write("\n")
print("wrote", out, len(GOLDEN), "keys")
