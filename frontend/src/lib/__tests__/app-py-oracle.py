"""
Generates app-py-oracle.json: what the Tk original answers, as data.

    python src/lib/__tests__/app-py-oracle.py src/lib/__tests__/app-py-oracle.json

Nothing here is retyped from the Svelte or the Go source. Every function below
is SLICED OUT of app.py by line number and exec'd under CPython, so the answers
in the fixture are the Tk program's own — which is the only thing that makes a
passing test mean "the port matches what it is a port of" rather than "the file
agrees with itself".

Read-only against app.py, and it imports nothing from it: the slices are exec'd
in throwaway namespaces with a stand-in MailAccount and a stand-in StringVar, so
no Tk, no state.json, no network. Set OPENAI_REGISTER_PYTHON_REFERENCE to the
legacy repository directory (or app.py itself), or check that repository out as
a sibling named openai-register-python-reference.

The line numbers are the contract. If app.py moves, the slices move with it —
`slice_lines(19004, 19011)` grabbing the wrong eight lines is a loud failure
(a NameError or a wildly different fixture), not a quiet one, but the citations
in the test files have to be updated too.
"""
import json
import os
import re
import sys
import textwrap
from pathlib import Path

configured_reference = os.environ.get("OPENAI_REGISTER_PYTHON_REFERENCE", "").strip()
if configured_reference:
    reference_path = Path(configured_reference).expanduser()
    APP = reference_path if reference_path.suffix.lower() == ".py" else reference_path / "app.py"
else:
    APP = Path(__file__).resolve().parents[4].parent / "openai-register-python-reference" / "app.py"

if not APP.is_file():
    print(
        "SKIP: 未找到旧版 Python app.py；请设置 OPENAI_REGISTER_PYTHON_REFERENCE，"
        "或将参考仓库放在当前仓库旁的 openai-register-python-reference 目录。",
        file=sys.stderr,
    )
    raise SystemExit(0)

SRC = APP.read_text(encoding="utf-8").split("\n")


def slice_lines(a, b):
    """app.py lines a..b inclusive, 1-based."""
    return "\n".join(SRC[a - 1 : b])


# ---------------------------------------------------------------- module level
CONSTS = {}
for lineno in (66, 72, 82, 83, 84, 282, 283, 296, 297, 305, 306, 307, 309, 316):
    exec(slice_lines(lineno, lineno), CONSTS)
exec(slice_lines(310, 315), CONSTS)  # ACCOUNT_SORT_LABELS
exec(slice_lines(317, 325), CONSTS)  # ACCOUNT_STATUS_FILTER_OPTIONS
# 12391-12393 sound seeds: drop the `self.` and the Var wrapper, keep the value.
exec(
    slice_lines(12391, 12393)
    .replace("        self.", "")
    .replace("BooleanVar(value=", "(")
    .replace("StringVar(value=", "("),
    CONSTS,
)

# ------------------------------------------------------- parse_account_line &c
PARSER_NS = {"re": re}


class MailAccount:  # stand-in for the dataclass; parse_account_line only sets fields
    def __init__(self, **kw):
        self.__dict__.update(kw)


PARSER_NS["MailAccount"] = MailAccount
exec(slice_lines(1610, 1614), PARSER_NS)  # normalize_email_address
exec(slice_lines(1645, 1692), PARSER_NS)  # extract_account_extras
exec(slice_lines(1617, 1642), PARSER_NS)  # parse_account_line

normalize_email_address = PARSER_NS["normalize_email_address"]
parse_account_line = PARSER_NS["parse_account_line"]

# ------------------------------------------------------------- prompt title
PROMPT_NS = {}
exec(
    "def prompt_title(prompt_type):\n"
    + textwrap.indent(textwrap.dedent(slice_lines(19004, 19011)), "    ")
    + "\n    return title\n",
    PROMPT_NS,
)
prompt_title = PROMPT_NS["prompt_title"]

# ------------------------------------------------- workbench filter/sort/search
WB_NS = dict(CONSTS)
WB_NS["re"] = re
WB_NS["MailAccount"] = MailAccount  # the slices annotate `account: MailAccount`


class _Var:
    def __init__(self, value):
        self._value = value

    def get(self):
        return self._value


CLASS_BODY = "\n".join(
    [
        "class Wb:",
        textwrap.indent(slice_lines(19057, 19076), "    "),   # _account_status_text, _session_has_k12_success
        textwrap.indent(slice_lines(19092, 19106), "    "),   # _account_attempt_count, _account_row_values, _account_sort_key
        textwrap.indent(slice_lines(19108, 19158), "    "),   # _account_visible_indices, _account_matches_status_filter
        textwrap.indent(slice_lines(19726, 19732), "    "),   # _account_display_indices
    ]
)
exec(CLASS_BODY, WB_NS)
Wb = WB_NS["Wb"]

# _toggle_account_sort (19045-19053) with its two side-effecting calls stubbed.
TOGGLE_NS = dict(CONSTS)
exec(
    "class Toggle:\n"
    + slice_lines(19045, 19053)  # already indented one level inside its class
    + "\n"
    + "    def _set_account_sort_state(self, column, direction):\n"
    + "        self.account_sort_column = column\n"
    + "        self.account_sort_direction = direction\n"
    + "    def _render_accounts(self, preserve_yview=False):\n        pass\n"
    + "    def save_state(self):\n        pass\n",
    TOGGLE_NS,
)
Toggle = TOGGLE_NS["Toggle"]

# ------------------------------------------------------------------- smsbower
SMS_NS = dict(CONSTS)
SMS_NS["re"] = re
exec("class Sms:\n" + textwrap.indent(slice_lines(14366, 14386), "    "), SMS_NS)
Sms = SMS_NS["Sms"]

# ------------------------------------------------------------- import grouping
IMPORT_NS = dict(CONSTS)
exec(
    "def import_group_for(active_group):\n"
    + "    "
    + slice_lines(14694, 14694).strip()
    + "\n    return import_group\n",
    IMPORT_NS,
)
import_group_for = IMPORT_NS["import_group_for"]


# ------------------------------------------------- 导出转换 group (app.py 13765)
# The Tk button captions and their handlers, read out of the _button_grid call
# rather than out of the preview-dialog titles those handlers later pass to
# _preview_and_save_text — the two are different strings for two of the six.
EXPORT_GROUP = []
for caption, handler, tooltip in re.findall(
    r'\(\s*"([^"]+)",\s*self\.(\w+),\s*"([^"]+)"\s*\)', slice_lines(13765, 13774)
):
    defline = next(
        (i for i, line in enumerate(SRC, 1) if line.strip().startswith(f"def {handler}(")),
        0,
    )
    EXPORT_GROUP.append({"label": caption, "handler": handler, "tooltip": tooltip, "handlerLine": defline})


# ============================================================== corpora
def parse_case(line):
    try:
        account = parse_account_line(line)
    except Exception as exc:  # noqa: BLE001 - app.py raises bare ValueError
        return {"line": line, "error": str(exc), "account": None}
    return {
        "line": line,
        "error": "",
        "account": {
            "email": account.email,
            "password": account.password,
            "clientID": account.client_id,
            "refreshToken": account.refresh_token,
            "accountType": account.account_type,
            "status": account.status,
            "openaiRT": account.openai_rt,
            "authPhoneNumber": account.auth_phone_number,
            "authPhoneSMSURL": account.auth_phone_sms_url,
            "receiveMailbox": account.receive_mailbox,
            "mailProvider": account.mail_provider,
        },
    }


PARSE_LINES = [
    # -- shape
    "a@b.com----pw----cid----rt",
    "a@b.com----pw----cid",
    "a@b.com----pw----cid----rt----",
    "----pw----cid----rt",
    "  a@b.com ---- pw ---- cid ---- rt  ",
    "a@b.com--------cid----rt",
    "a@b.com----pw--------rt",
    "a@b.com----pw----cid----",
    "not-an-email----pw----cid----rt",
    # -- cloudmail exemption
    "a@b.com----pw--------  ----mail_provider=cloudmail",
    "a@b.com----pw--------  ----mail_provider=CloudMail",
    "a@b.com----pw--------  ----mail_provider=outlook",
    "a@b.com----pw--------  ----mail_provider=gmail",
    "a@b.com----pw--------  ----mail_type=cloudmail",
    # -- email normalisation inside field 0
    '"a@b.com"----pw----cid----rt',
    "<a@b.com>----pw----cid----rt",
    "（a@b.com）----pw----cid----rt",
    "，a@b.com；----pw----cid----rt",
    "name a@b.com tail----pw----cid----rt",
    "A@B.COM----pw----cid----rt",
    "a@b----pw----cid----rt",
    "[a@b.com]----pw----cid----rt",
    # -- rt prefixes
    "a@b.com----pw----cid----rt----rt_token=RT1",
    "a@b.com----pw----cid----rt----openai_rt=RT2",
    "a@b.com----pw----cid----rt----RT_TOKEN=RT3",
    "a@b.com----pw----cid----rt----rt_token=  RT4  ",
    "a@b.com----pw----cid----rt----rt_token=a=b",
    "a@b.com----pw----cid----rt----rt_token=",
    # -- phone prefixes
    "a@b.com----pw----cid----rt----auth_phone=+33123456",
    "a@b.com----pw----cid----rt----auth_phone_number=+33123456",
    "a@b.com----pw----cid----rt----phone=+33123456",
    "a@b.com----pw----cid----rt----auth_phone_sms_url=https://sms.example/1",
    "a@b.com----pw----cid----rt----auth_sms_url=https://sms.example/2",
    "a@b.com----pw----cid----rt----phone_sms_url=https://sms.example/3",
    "a@b.com----pw----cid----rt----sms_url=https://sms.example/4",
    # -- receive mailbox
    "a@b.com----pw----cid----rt----receive_mailbox=<c@d.com>",
    "a@b.com----pw----cid----rt----mailbox_email=c@d.com",
    "a@b.com----pw----cid----rt----receive_email= c@d.com ",
    "a@b.com----pw----cid----rt----inbox=c@d.com",
    "a@b.com----pw----cid----rt----inbox=junk",
    # -- account type
    "a@b.com----pw----cid----rt----account_type=team",
    "a@b.com----pw----cid----rt----type=plus",
    "a@b.com----pw----cid----rt----type=FREE",
    "a@b.com----pw----cid----rt----type=k12",
    "a@b.com----pw----cid----rt----type=pro",
    "a@b.com----pw----cid----rt----type=",
    "a@b.com----pw----cid----rt----type=team----rt_token=RT",
    # -- positional fallbacks
    "a@b.com----pw----cid----rt----+33 1 23 45 67https://sms.example/x",
    "a@b.com----pw----cid----rt----+331234567",
    "a@b.com----pw----cid----rt----12345",
    "a@b.com----pw----cid----rt----123456",
    "a@b.com----pw----cid----rt----https://sms.example/y",
    "a@b.com----pw----cid----rt----http://sms.example/y",
    "a@b.com----pw----cid----rt----ftp://sms.example/y",
    "a@b.com----pw----cid----rt----+331234567----https://sms.example/z",
    "a@b.com----pw----cid----rt----phone=+1----+339999999",
    "a@b.com----pw----cid----rt----sms_url=https://a/1----https://b/2",
    "a@b.com----pw----cid----rt----+331234567----+339999999",
    "a@b.com----pw----cid----rt----(33) 12.34-56",
    "a@b.com----pw----cid----rt----garbage----https://sms.example/w",
    # -- status derivation
    "a@b.com----pw----cid----rt----phone=+331----sms_url=https://s/1",
    "a@b.com----pw----cid----rt----phone=+331",
    "a@b.com----pw----cid----rt----rt_token=RT----phone=+331----sms_url=https://s/1",
    # -- whitespace / empties among extras
    "a@b.com----pw----cid----rt----   ----rt_token=RT",
    "a@b.com----pw----cid----rt----\ttype=plus\t",
]

# Unicode-digit probes are kept apart: Python's `\d` is Unicode-aware and JS's is
# not, so these are the cases where the port is EXPECTED to differ.
PARSE_LINES_UNICODE = [
    "a@b.com----pw----cid----rt----１２３４５６７",
    "a@b.com----pw----cid----rt----٣٣١٢٣٤٥٦",
]

PARSE_TEXTS = [
    "a@b.com----pw----cid----rt",
    "\n\na@b.com----pw----cid----rt\n\n\nbad-line\n\n",
    "   \n\t\n",
    "",
    "bad\nalso-bad----x",
    "a@b.com----pw----cid----rt\r\nc@d.com----pw----cid----rt\r\n",
    "one----two\n\nb@c.com----pw----cid----rt\nthree",
]


def parse_text_case(text):
    # app.py:14687 + 14695 — blank lines are dropped BEFORE numbering.
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    rows = []
    for index, line in enumerate(lines, start=1):
        case = parse_case(line)
        rows.append({"line": index, "error": case["error"], "account": case["account"]})
    return {"text": text, "rows": rows}


NORMALIZE_EMAILS = [
    "a@b.com",
    "  a@b.com  ",
    '"a@b.com"',
    "'a@b.com'",
    "“a@b.com”",
    "‘a@b.com’",
    "<a@b.com>",
    "(a@b.com)",
    "[a@b.com]",
    "{a@b.com}",
    "，a@b.com，",
    ",a@b.com,",
    ";a@b.com；",
    "\ta@b.com\r\n",
    "junk",
    "",
    "()",
    "a b@c.com d",
    "b@c.co",
    "b@c.c",
    "first@x.com second@y.com",
    "汉字a@b.com汉字",
    "a@b.com.",
]

# --- workbench rows ---------------------------------------------------------
ROWS = [
    # (email, account_type, status, group, openai_rt, phone, sms_url, link, session, attempts)
    ("zoe@example.com", "free", "", "", "", "", "", "", "", 0),
    ("amy@example.com", "Plus", "", "组A", "", "", "", "", "tok", 3),
    ("bob@example.com", "pro", "", "组A", "", "", "", "https://pay/1", "", 1),
    ("cat@example.com", "team", "", "", "", "", "", "", "", 12),
    ("dan@example.com", "free", "登录失败", "组B", "", "", "", "", "", 2),
    ("eve@example.com", "free", "代理耗尽", "", "", "", "", "", "", 0),
    ("fay@example.com", "FREE", "疑似已封禁", "组B", "", "", "", "", "", 0),
    ("gus@example.com", "plus", "", "", "RT", "+331", "https://s/1", "", "", 0),
    ("HAL@example.com", "free", "", "未分组", "", "+331", "https://s/1", "", "", 5),
    ("ivy@example.com", "", "", "组A", "", "", "", "", "", 0),
]


def build_wb():
    wb = Wb()
    wb.accounts = []
    wb.results = {}
    wb.session_results = {}
    wb.link_attempt_counts = {}
    for email, atype, status, group, rt, phone, sms, link, session, attempts in ROWS:
        account = MailAccount(
            email=email,
            account_type=atype,
            status=status,
            group=group,
            openai_rt=rt,
            auth_phone_number=phone,
            auth_phone_sms_url=sms,
        )
        wb.accounts.append(account)
        if link:
            wb.results[email] = link
        if session:
            wb.session_results[email] = {"access_token": session}
        if attempts:
            wb.link_attempt_counts[email] = attempts
    return wb


def row_view(wb, account):
    payload = wb.session_results.get(account.email, {})
    has_session = isinstance(payload, dict) and bool(
        str(payload.get("access_token") or payload.get("session_json") or "").strip()
    )
    return {
        "email": account.email,
        "account_type": account.account_type,
        "group": account.group,
        "statusText": wb._account_status_text(account),
        "hasSession": has_session,
        "link": str(wb.results.get(account.email, "") or ""),
        "attempts": wb._account_attempt_count(account),
    }


WB = build_wb()
ROW_VIEWS = [row_view(WB, account) for account in WB.accounts]

GROUPS = [CONSTS["ACCOUNT_ALL_GROUP"], "组A", "组B", CONSTS["ACCOUNT_DEFAULT_GROUP"], "组X"]
STATUS_FILTERS = list(CONSTS["ACCOUNT_STATUS_FILTER_OPTIONS"]) + ["无此过滤器", ""]
SEARCHES = [
    "",
    "   ",
    "example",
    "AMY",
    "amy plus",
    "plus amy",
    "amy team",
    "组A",
    "未分组",
    "\t失败\n",
    "hal",
    "  a   e  ",
    "撞",
    "free 组B",
    "nope",
]


def visible_case(group, status_filter, search):
    wb = build_wb()
    wb.account_group_filter = _Var(group)
    wb.account_status_filter = _Var(status_filter)
    wb.account_search = _Var(search)
    # Indices into `rows`, not emails: the matrix is 675 cases and the whole
    # point of the fixture is that it stays readable in a diff.
    return {
        "group": group,
        "statusFilter": status_filter,
        "search": search,
        "visible": wb._account_visible_indices(),
    }


def sort_case(column, direction):
    wb = build_wb()
    wb.account_group_filter = _Var(CONSTS["ACCOUNT_ALL_GROUP"])
    wb.account_status_filter = _Var(CONSTS["ACCOUNT_STATUS_FILTER_ALL"])
    wb.account_search = _Var("")
    wb.account_sort_column = column
    wb.account_sort_direction = direction
    indices = wb._account_display_indices()
    return {
        "column": column,
        "direction": direction,
        "emails": [wb.accounts[i].email for i in indices],
    }


def toggle_case(column, direction, clicked):
    toggle = Toggle()
    toggle.account_sort_column = column
    toggle.account_sort_direction = direction
    toggle._toggle_account_sort(clicked)
    return {
        "from": {"column": column, "direction": direction},
        "clicked": clicked,
        "to": {"column": toggle.account_sort_column, "direction": toggle.account_sort_direction},
    }


# --- smsbower ---------------------------------------------------------------
SMS_CASES = [
    # (enabled, api_key, service, country, max_price)
    (False, "", "dr", "33", "0.07"),
    (False, "", "", "", ""),
    (False, "", "  ", "  ", "  "),
    (False, "", "ab_09", "187", "1"),
    (False, "", "dr!", "33", ""),
    (False, "", "dr dr", "33", ""),
    (False, "", "汉", "33", ""),
    (False, "", "dr", "33a", ""),
    (False, "", "dr", "-1", ""),
    (False, "", "dr", "33", "0"),
    (False, "", "dr", "33", "-0.5"),
    (False, "", "dr", "33", "abc"),
    (False, "", "dr", "33", "0.07 "),
    (False, "", "dr", "33", ".5"),
    (False, "", "dr", "33", "5."),
    (False, "", "dr", "33", "1e-3"),
    (False, "", "dr", "33", "+1"),
    (False, "", "dr", "33", "0.0"),
    # Unicode decimal digits (Nd): Python's isdigit()/float() take them and so
    # does the port — \p{Nd} and pyDecimalASCII. These belong in the AGREEING
    # corpus precisely because it would be easy to assume they do not.
    (False, "", "dr", "٣٣", ""),
    (False, "", "dr", "33", "１.５"),
    (True, "", "dr", "33", ""),
    (True, "  ", "dr", "33", ""),
    (True, " key ", "dr", "33", ""),
    (True, "key", "dr!", "33", ""),
]

# Cases where CPython and JS are expected to part company; kept separate so the
# agreeing corpus above stays a clean equality assertion.
SMS_CASES_DIVERGENT = [
    # str.isdigit() is True for Numeric_Type=Digit, which includes superscripts
    # that are NOT category Nd.
    (False, "", "dr", "²", ""),
    # float() grammar extras the editor refuses on purpose.
    (False, "", "dr", "33", "1_0"),
    (False, "", "dr", "33", "inf"),
    (False, "", "dr", "33", "nan"),
]


def sms_case(enabled, api_key, service, country, max_price):
    app = Sms()
    app.smsbower_enabled = _Var(enabled)
    app.smsbower_api_key = _Var(api_key)
    app.smsbower_service = _Var(service)
    app.smsbower_country = _Var(country)
    app.smsbower_max_price = _Var(max_price)
    try:
        settings = app._smsbower_settings()
    except ValueError as exc:  # 14371 / 14373 / 14379
        return {"input": [enabled, api_key, service, country, max_price], "error": str(exc), "normalized": None}
    error = ""
    if settings["enabled"] and not settings["api_key"]:  # 14391
        error = "启用 SMSBower 前请填写 API Key"
    return {
        "input": [enabled, api_key, service, country, max_price],
        "error": error,
        "normalized": {
            "enabled": settings["enabled"],
            "apiKey": settings["api_key"],
            "service": settings["service"],
            "country": settings["country"],
            "maxPrice": settings["max_price"],
        },
    }


OUT = {
    "_generated_by": "src/lib/__tests__/app-py-oracle.py — verbatim app.py slices under CPython "
    + sys.version.split()[0]
    + ". GENERATED; do not hand-edit.",
    "constants": {
        "DEFAULT_PAYPAL_EXTENSION_DIR": CONSTS["DEFAULT_PAYPAL_EXTENSION_DIR"],
        "AUDIO_DEFAULT_DEVICE_LABEL": CONSTS["AUDIO_DEFAULT_DEVICE_LABEL"],
        "SMSBOWER_DEFAULT_SERVICE": CONSTS["SMSBOWER_DEFAULT_SERVICE"],
        "SMSBOWER_DEFAULT_COUNTRY": CONSTS["SMSBOWER_DEFAULT_COUNTRY"],
        "SMSBOWER_DEFAULT_MAX_PRICE": CONSTS["SMSBOWER_DEFAULT_MAX_PRICE"],
        "PROXY_ROUTE_MODE_DEFAULT": CONSTS["PROXY_ROUTE_MODE_DEFAULT"],
        "PROXY_ROUTE_MODE_LOCAL_ONLY": CONSTS["PROXY_ROUTE_MODE_LOCAL_ONLY"],
        "ACCOUNT_ALL_GROUP": CONSTS["ACCOUNT_ALL_GROUP"],
        "ACCOUNT_DEFAULT_GROUP": CONSTS["ACCOUNT_DEFAULT_GROUP"],
        "ACCOUNT_SORT_COLUMNS": list(CONSTS["ACCOUNT_SORT_COLUMNS"]),
        "ACCOUNT_SORT_LABELS": CONSTS["ACCOUNT_SORT_LABELS"],
        "ACCOUNT_STATUS_FILTER_ALL": CONSTS["ACCOUNT_STATUS_FILTER_ALL"],
        "ACCOUNT_STATUS_FILTER_OPTIONS": list(CONSTS["ACCOUNT_STATUS_FILTER_OPTIONS"]),
        # 19153 is a local tuple inside _account_matches_status_filter.
        "FAILURE_WORDS": list(eval(slice_lines(19153, 19153).split("=", 1)[1].strip())),
        # 13885-13888 Treeview column widths.
        "ACCOUNT_COLUMN_WIDTHS": {
            m.group(1): int(m.group(2))
            for m in re.finditer(
                r'self\.account_list\.column\("(\w+)", width=(\d+)', slice_lines(13885, 13888)
            )
        },
        "SOUND_SEEDS": {
            "success_sound_enabled": CONSTS["success_sound_enabled"],
            "success_audio_device": CONSTS["success_audio_device"],
            "pause_others_on_link_success": CONSTS["pause_others_on_link_success"],
        },
    },
    "exportGroup": EXPORT_GROUP,
    "promptTitles": [
        {"kind": kind, "title": prompt_title(kind)}
        for kind in [
            "phone",
            "phone-code",
            "sms-code",
            "email-code",
            "email-otp",
            "",
            "Phone",
            "PHONE",
            "unknown",
            "email_code",
            "phone code",
        ]
    ],
    "normalizeEmail": [{"input": v, "output": normalize_email_address(v)} for v in NORMALIZE_EMAILS],
    "parseLine": [parse_case(line) for line in PARSE_LINES],
    "parseLineUnicodeDigits": [parse_case(line) for line in PARSE_LINES_UNICODE],
    "parseText": [parse_text_case(text) for text in PARSE_TEXTS],
    "importGroup": [
        {"active": g, "group": import_group_for(g)}
        for g in [CONSTS["ACCOUNT_ALL_GROUP"], CONSTS["ACCOUNT_DEFAULT_GROUP"], "组A", "", "全部 "]
    ],
    "rows": ROW_VIEWS,
    # group x status with no search, then search on its own, then the AND of all
    # three. The three predicates are independent in app.py 19117-19133, so the
    # full 5x9x15 product buys nothing over this and costs a 70 KB fixture.
    "visible": (
        [visible_case(g, s, "") for g in GROUPS for s in STATUS_FILTERS]
        + [visible_case(CONSTS["ACCOUNT_ALL_GROUP"], CONSTS["ACCOUNT_STATUS_FILTER_ALL"], q) for q in SEARCHES]
        + [
            visible_case("组A", "有 Session", "amy"),
            visible_case("组A", "有 Session", "bob"),
            visible_case("组B", "失败", "登录"),
            visible_case("组B", "失败", "nope"),
            visible_case(CONSTS["ACCOUNT_DEFAULT_GROUP"], "待处理", "hal"),
            visible_case(CONSTS["ACCOUNT_ALL_GROUP"], "Plus", "example"),
            visible_case(CONSTS["ACCOUNT_ALL_GROUP"], "提链成功", "pro"),
            visible_case("组X", CONSTS["ACCOUNT_STATUS_FILTER_ALL"], ""),
        ]
    ),
    "sorted": [
        sort_case(c, d) for c in list(CONSTS["ACCOUNT_SORT_COLUMNS"]) + ["nope"] for d in ["custom", "asc", "desc"]
    ],
    "toggle": [
        toggle_case(c, d, clicked)
        for c in CONSTS["ACCOUNT_SORT_COLUMNS"]
        for d in ["custom", "asc", "desc"]
        for clicked in CONSTS["ACCOUNT_SORT_COLUMNS"]
    ],
    "smsbower": [sms_case(*case) for case in SMS_CASES],
    "smsbowerDivergent": [sms_case(*case) for case in SMS_CASES_DIVERGENT],
}

text = json.dumps(OUT, ensure_ascii=False, indent=2)
# Collapse pure-integer arrays (the visible-index lists) onto one line so the
# fixture stays diffable instead of 20k lines of single digits.
text = re.sub(r"\[\s+((?:\d+,\s+)*\d+)\s+\]", lambda m: "[" + re.sub(r"\s+", " ", m.group(1)) + "]", text)
text = text.replace("[\n\n      ]", "[]")

target = Path(sys.argv[1])
target.parent.mkdir(parents=True, exist_ok=True)
target.write_text(text + "\n", encoding="utf-8")
print("wrote", target)
