#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Executive Office (EO) pilot demo dataset.

Fills all eleven concrete EO registers with a coherent, presentable story in
Saudi municipal terms, THROUGH THE HTTP API — never by SQL INSERT — so every
record passes the real validation, numbering, link and workflow paths and
carries a reference number and a history.

Run:
    python3 backend/scripts/eo-demo-data.py
    API_BASE=http://localhost:8080/api/v1 EO_ADMIN_EMAIL=... EO_ADMIN_PASSWORD=... python3 ...

IDEMPOTENCY. Re-running is safe. Departments are matched by name, records by
(record type, subject_en); an existing one is reused instead of being created
again, and a transition is run only when the record is actually sitting in the
transition's from-state. So a second run creates nothing and changes nothing.
It does NOT clean up: records this script created earlier under a subject that
has since been edited will be left alone and a new one created beside them.

WHAT IT CANNOT FILL, and why: EO Documenta is disabled in the pilot
deployment, so every `file` / `multi_file` field (attachments, evidence,
brief_file, draft_file, study_report, proof, reply_files, response_files) has
required_mode=never and no id can be minted. They are left empty on purpose.
"""

import json
import os
import sys
import urllib.error
import urllib.request

API = os.environ.get("API_BASE", "http://localhost:8080/api/v1")
EMAIL = os.environ.get("EO_ADMIN_EMAIL", "admin@automax.com")
PASSWORD = os.environ.get("EO_ADMIN_PASSWORD", "admin123")

TOKEN = None
VERBOSE = os.environ.get("EO_DEMO_VERBOSE", "1") == "1"


# ── HTTP ────────────────────────────────────────────────────────────────────

class ApiError(Exception):
    def __init__(self, status, body):
        super().__init__(f"HTTP {status}: {body}")
        self.status = status
        self.body = body


def call(method, path, payload=None):
    url = API + path
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if TOKEN:
        req.add_header("Authorization", "Bearer " + TOKEN)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        raise ApiError(e.code, e.read().decode()) from None


def login():
    global TOKEN
    out = call("POST", "/auth/login", {"email": EMAIL, "password": PASSWORD})
    TOKEN = out["data"]["token"]
    return out["data"]["user"]


def log(*a):
    if VERBOSE:
        print(*a, flush=True)


# ── catalogue ───────────────────────────────────────────────────────────────

def record_types():
    return {t["key"]: t for t in call("GET", "/admin/eo-record-types")["data"]}


def list_all_records():
    out, page = [], 1
    while True:
        res = call("GET", f"/eo-records?page={page}&limit=100")
        out.extend(res.get("data") or [])
        if page >= (res.get("total_pages") or 1):
            return out
        page += 1


def list_departments():
    return call("GET", "/admin/departments?limit=100")["data"]


# ── upserts ─────────────────────────────────────────────────────────────────

DEPTS = {}        # slug -> id
RECORDS = {}      # slug -> record dict
_dept_by_name = {}
_rec_by_key = {}  # (record_type_id, subject_en) -> record


def ensure_dept(slug, name, name_ar, kind):
    if name in _dept_by_name:
        DEPTS[slug] = _dept_by_name[name]["id"]
        return DEPTS[slug]
    body = {"name": name, "name_ar": name_ar, "type": kind}
    dep = call("POST", "/admin/departments", body)["data"]
    _dept_by_name[name] = dep
    DEPTS[slug] = dep["id"]
    log(f"  + department {name} ({kind})")
    return dep["id"]


def ensure_record(slug, type_key, values, parent=None, link_key=None):
    """Create an EO record, or reuse the one with the same type + subject_en."""
    tid = TYPES[type_key]["id"]
    key = (tid, values.get("subject_en", ""))
    if key in _rec_by_key:
        RECORDS[slug] = _rec_by_key[key]
        return _rec_by_key[key]
    body = {"record_type_id": tid, "values": values}
    if parent:
        body["parent_record_id"] = RECORDS[parent]["id"]
        body["parent_link_type_key"] = link_key
    rec = call("POST", "/eo-records", body)["data"]
    _rec_by_key[key] = rec
    RECORDS[slug] = rec
    log(f"  + {type_key:28} {rec['reference']:24} {values.get('subject_en','')[:58]}")
    return rec


def state_code(rec):
    return STATE_CODE.get(rec.get("state_id"))


def reachable(type_id, state_id):
    """Every state the record could still get to from where it stands."""
    seen, stack = set(), [state_id]
    while stack:
        s = stack.pop()
        for nxt in GRAPH.get((type_id, s), ()):
            if nxt not in seen:
                seen.add(nxt)
                stack.append(nxt)
    return seen


def run(slug, codes, comment, values=None):
    """Walk a record through the named transition codes, skipping any step it
    has already taken. values is {transition_code: {...}} for the transitions
    that declare field changes.

    A step is skipped when the record already SITS IN that transition's target
    state, and also when the target is no longer reachable from where it
    stands — which, in these linear EO workflows, means it has already gone
    past it on an earlier run. That is what makes a re-run a no-op instead of
    a 409 against a closed record."""
    rec = RECORDS[slug]
    for code in codes:
        fresh = call("GET", f"/eo-records/{rec['id']}")["data"]
        tid, here = fresh["record_type_id"], fresh.get("state_id")
        target = TARGET_OF.get((tid, code))
        if here == target:
            continue  # already there
        if target is not None and target not in reachable(tid, here):
            continue  # already past it
        avail = call("GET", f"/eo-records/{rec['id']}/transitions")["data"]
        match = next((t for t in avail if t["code"] == code), None)
        if match is None:
            raise ApiError(409, f"{fresh['reference']}: transition {code!r} not available "
                                f"from state {state_code(fresh)!r} "
                                f"(offered: {[t['code'] for t in avail]})")
        body = {"transition_id": match["id"], "comment": comment}
        if values and code in values:
            body["values"] = values[code]
        call("POST", f"/eo-records/{rec['id']}/transition", body)
        log(f"  > {fresh['reference']:24} {code}")
    RECORDS[slug] = call("GET", f"/eo-records/{rec['id']}")["data"]


# ── the dataset ─────────────────────────────────────────────────────────────

def main():
    global TYPES, STATE_CODE, TARGET_OF, GRAPH

    user = login()
    me = user["id"]
    TYPES = record_types()

    # Workflow states/transitions, so `run` can tell "already done" from
    # "not offered". Read from the catalogue, never hard-coded.
    STATE_CODE, TARGET_OF, GRAPH = {}, {}, {}
    for t in TYPES.values():
        wf = t.get("workflow_id")
        if not wf:
            continue
        for s in call("GET", f"/admin/workflows/{wf}/states")["data"]:
            STATE_CODE[s["id"]] = s["code"]
        for tr in call("GET", f"/admin/workflows/{wf}/transitions")["data"]:
            TARGET_OF[(t["id"], tr["code"])] = tr["to_state_id"]
            GRAPH.setdefault((t["id"], tr["from_state_id"]), set()).add(tr["to_state_id"])

    for d in list_departments():
        _dept_by_name[d["name"]] = d
    for r in list_all_records():
        _rec_by_key[(r["record_type_id"], r.get("subject_en") or "")] = r

    log("Departments")
    eo = _dept_by_name.get("Executive Office")
    DEPTS["eo"] = eo["id"] if eo else ensure_dept(
        "eo", "Executive Office", "المكتب التنفيذي", "internal")
    # Internal bodies a referral or an assignment can go to.
    ensure_dept("roads", "Roads and Transport Agency", "وكالة الطرق والنقل", "internal")
    ensure_dept("services", "Municipal Services Agency", "وكالة الخدمات البلدية", "internal")
    ensure_dept("legal", "Legal Affairs Department", "الإدارة العامة للشؤون القانونية", "internal")
    ensure_dept("planning", "Urban Planning Agency", "وكالة التخطيط العمراني", "internal")
    # External bodies: the only values an `external_body` field accepts are
    # departments of type "external" (the picker filters on it).
    ensure_dept("momrah", "Ministry of Municipal and Rural Affairs and Housing",
                "وزارة الشؤون البلدية والقروية والإسكان", "external")
    ensure_dept("emirate", "Riyadh Region Emirate", "إمارة منطقة الرياض", "external")
    ensure_dept("traffic", "General Department of Traffic",
                "الإدارة العامة للمرور", "external")
    ensure_dept("health", "Ministry of Health", "وزارة الصحة", "external")

    D = DEPTS

    # ── 1. Incoming correspondence ─────────────────────────────────────────
    log("Incoming correspondence")
    ensure_record("letter_parking", "eo_incoming_correspondence", {
        "subject_ar": "توحيد تعرفة مواقف السيارات في النطاق المركزي",
        "subject_en": "Unifying public parking tariffs in the central district",
        "priority": "high", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2026-10-15",
        "sender": D["momrah"], "sender_ref": "MOMRAH-2026-14420",
        "letter_date": "2026-09-21", "received_date": "2026-09-24",
        "real_ask": "موافاة الوزارة بموقف موحد من مقترح توحيد تعرفة مواقف السيارات في النطاق المركزي، "
                    "متضمناً الأثر المالي المتوقع والجدول الزمني للتطبيق.",
        "affected_bodies": [D["roads"], D["legal"], D["planning"]],
        "brief_view": "المقترح يقع ضمن اختصاص الأمانة، ويتطلب رأي وكالة الطرق والنقل في الأثر التشغيلي "
                      "ورأي الشؤون القانونية في اللائحة النافذة.",
        "brief_recommendation": "الإحالة إلى وكالة الطرق والنقل وإلى الإدارة العامة للشؤون القانونية، "
                                "وتولي وكالة الطرق والنقل تجميع الردود في موقف واحد.",
        "direction_pattern": "refer_and_consolidate", "coordinator": D["roads"],
        "direction_text": "يحال إلى الجهتين المذكورتين لإبداء المرئيات خلال ثلاثة أيام، "
                          "وترفع الوكالة الموقف الموحد للمكتب التنفيذي.",
        "consolidated": "اتفقت الجهتان على التدرج في تطبيق التعرفة الموحدة على ثلاث مراحل مع إعفاء "
                        "ساعة الوقوف الأولى، وعلى تعديل المادة الرابعة من اللائحة قبل التطبيق.",
    })

    ensure_record("letter_school", "eo_incoming_correspondence", {
        "subject_ar": "رفع مستوى السلامة المرورية في محيط المدارس",
        "subject_en": "Raising road-safety standards around schools",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2026-11-05",
        "sender": D["traffic"], "sender_ref": "GDT-2026-7781",
        "letter_date": "2026-10-12", "received_date": "2026-10-14",
        "real_ask": "دراسة مواقع العبور المدرسي في أربعة أحياء وتزويد الإدارة العامة للمرور "
                    "بخطة معالجة التقاطعات الخطرة.",
        "affected_bodies": [D["roads"], D["planning"]],
        "brief_view": "الملاحظات الواردة تخص ستة تقاطعات، أربعة منها ضمن أعمال صيانة قائمة.",
        "brief_recommendation": "الإحالة إلى وكالة الطرق والنقل لإعداد دراسة تفصيلية خلال أسبوعين.",
        "direction_pattern": "refer_out",
        "direction_text": "تحال إلى وكالة الطرق والنقل لإعداد الدراسة ورفعها للمكتب التنفيذي.",
        "consolidated": "وردت دراسة وكالة الطرق والنقل متضمنة ستة تقاطعات ومقترح معالجة لكل منها.",
    })

    ensure_record("letter_rain", "eo_incoming_correspondence", {
        "subject_ar": "جاهزية تصريف مياه الأمطار قبل موسم الشتاء",
        "subject_en": "Storm-water drainage readiness ahead of the winter season",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2026-11-20",
        "sender": D["emirate"], "sender_ref": "RRE-2026-9033",
        "letter_date": "2026-10-28", "received_date": "2026-10-29",
        "real_ask": "رفع تقرير عن جاهزية شبكات تصريف مياه الأمطار ونقاط التجمعات وخطة "
                    "الاستجابة الميدانية قبل الخامس من ديسمبر.",
        "affected_bodies": [D["services"], D["roads"]],
        "brief_view": "التقرير يحتاج بيانات ميدانية من وكالة الخدمات البلدية وبيانات الطرق المغمورة سابقاً.",
        "brief_recommendation": "الإحالة إلى وكالتي الخدمات البلدية والطرق والنقل مع تكليف الأولى بالتجميع.",
        "direction_pattern": "refer_and_consolidate", "coordinator": D["services"],
        "direction_text": "تحال إلى الوكالتين لرفع الجاهزية، وتتولى وكالة الخدمات البلدية إعداد التقرير الموحد.",
        "consolidated": "قيد التجميع: ورد رد وكالة الخدمات البلدية، وما زال رد وكالة الطرق والنقل متوقعاً.",
    })

    ensure_record("letter_food", "eo_incoming_correspondence", {
        "subject_ar": "تكثيف الرقابة على المنشآت الغذائية",
        "subject_en": "Intensifying inspection of food establishments",
        "priority": "normal", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2026-12-10",
        "sender": D["health"], "sender_ref": "MOH-2026-3312",
        "letter_date": "2026-11-02", "received_date": "2026-11-04",
        "real_ask": "رفع معدل الزيارات التفتيشية على المطاعم والمطابخ المركزية بنسبة ٣٠٪ "
                    "وموافاة الوزارة بتقرير شهري.",
        "affected_bodies": [D["services"]],
        "brief_view": "الطلب ضمن قدرة فرق الرقابة الحالية بإعادة توزيع الجدول الأسبوعي.",
        "brief_recommendation": "الاكتفاء بالمعالجة داخل المكتب وإعداد رد يوضح آلية التنفيذ.",
        "direction_pattern": "finish_in_office",
        "direction_text": "يعد المكتب التنفيذي الرد بالتنسيق مع وكالة الخدمات البلدية دون إحالة رسمية.",
        "consolidated": "لا تنطبق الإحالة: عولج الطلب داخل المكتب التنفيذي.",
    })

    ensure_record("letter_report", "eo_incoming_correspondence", {
        "subject_ar": "تعميم تقرير الأداء الربعي للأمانات",
        "subject_en": "Circular: quarterly performance report for municipalities",
        "priority": "normal", "sensitivity": "public", "source": "correspondence",
        "owning_body_id": D["eo"], "due_date": "2026-12-31",
        "sender": D["momrah"], "sender_ref": "MOMRAH-2026-15990",
        "letter_date": "2026-11-16", "received_date": "2026-11-18",
        "real_ask": "الإحاطة بنتائج تقرير الأداء الربعي الثالث ولا يتطلب رداً.",
        "affected_bodies": [D["eo"]],
        "brief_view": "تعميم للعلم، لا يتضمن طلباً إجرائياً.",
        "brief_recommendation": "الحفظ للعلم وتعميمه على الوكالات.",
        "direction_pattern": "file_for_information",
        "direction_text": "يحفظ للعلم ويعمم على الوكالات عبر البريد الإداري.",
        "consolidated": "لا يوجد: حفظ للعلم.",
    })

    # ── 2. Referrals ───────────────────────────────────────────────────────
    log("Referrals")
    ensure_record("ref_parking_roads", "eo_referral", {
        "subject_ar": "إحالة: مرئيات وكالة الطرق والنقل في تعرفة المواقف",
        "subject_en": "Referral: Roads and Transport position on the parking tariff",
        "priority": "high", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "referred_body": D["roads"], "referral_owner": me, "ask_type": "position",
        "instruction": "إبداء المرئيات في الأثر التشغيلي لتوحيد التعرفة وفي الجدول الزمني المقترح للتطبيق.",
        "response": "لا مانع من التوحيد على ثلاث مراحل تبدأ بالنطاق المركزي، مع إعفاء ساعة الوقوف الأولى "
                    "وتحديث اللوحات الإرشادية قبل التطبيق.",
    }, parent="letter_parking", link_key="eo_referral_parent")

    ensure_record("ref_parking_legal", "eo_referral", {
        "subject_ar": "إحالة: الرأي القانوني في تعديل لائحة المواقف",
        "subject_en": "Referral: legal opinion on amending the parking by-law",
        "priority": "high", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "referred_body": D["legal"], "referral_owner": me, "ask_type": "deeper_study",
        "instruction": "بيان ما إذا كان توحيد التعرفة يستلزم تعديل اللائحة النافذة، وتحديد المواد المتأثرة.",
        "response": "التوحيد يستلزم تعديل المادة الرابعة من لائحة المواقف، ولا يستلزم موافقة مجلس الأمانة "
                    "ما دامت التعرفة ضمن الحد الأعلى المعتمد.",
    }, parent="letter_parking", link_key="eo_referral_parent")

    ensure_record("ref_school_roads", "eo_referral", {
        "subject_ar": "إحالة: دراسة تقاطعات محيط المدارس",
        "subject_en": "Referral: study of intersections around schools",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "referred_body": D["roads"], "referral_owner": me, "ask_type": "deeper_study",
        "instruction": "حصر التقاطعات الخطرة في الأحياء الأربعة ورفع مقترح معالجة لكل تقاطع مع التكلفة التقديرية.",
        "response": "تم حصر ستة تقاطعات؛ أربعة تعالج بمطبات ولوحات ضوئية ضمن عقد الصيانة القائم، "
                    "واثنان يحتاجان إشارة ضوئية جديدة بتكلفة تقديرية ٨٥٠ ألف ريال.",
    }, parent="letter_school", link_key="eo_referral_parent")

    ensure_record("ref_rain_services", "eo_referral", {
        "subject_ar": "إحالة: جاهزية شبكات التصريف وفرق الاستجابة",
        "subject_en": "Referral: drainage network readiness and response teams",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "referred_body": D["services"], "referral_owner": me, "ask_type": "position",
        "instruction": "رفع تقرير جاهزية الشبكات ونقاط التجمعات وخطة فرق الاستجابة الميدانية.",
        "response": "جاهزية الشبكات ٨٢٪؛ تم تنظيف ٦٤٠ من أصل ٧٨٠ نقطة تصريف، وخصصت ١٢ فرقة استجابة "
                    "موزعة على خمسة قطاعات.",
    }, parent="letter_rain", link_key="eo_referral_parent")

    # A SUB-referral: it hangs off the letter like every other branch (the
    # reference pattern IN-YYYY-NNNN-R# allows no deeper parent), and names the
    # branch it descends from in `parent_referral` — which is what the
    # referral_tree capability reads to shape the tree.
    ensure_record("ref_rain_roads", "eo_referral", {
        "subject_ar": "إحالة فرعية: الطرق المعرضة للغمر ضمن تقرير الجاهزية",
        "subject_en": "Sub-referral: flood-prone roads within the readiness report",
        "priority": "high", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "referred_body": D["roads"], "referral_owner": me, "ask_type": "information",
        "instruction": "موافاة وكالة الخدمات البلدية بقائمة الطرق التي غمرت في موسم ٢٠٢٥ وحالة معالجتها.",
        "response": "لم يرد بعد: المهلة المحتسبة من الإحالة قائمة وجاري حصر الطرق من سجلات الموسم الماضي.",
        "parent_referral": RECORDS["ref_rain_services"]["id"],
    }, parent="letter_rain", link_key="eo_referral_parent")

    # ── 3. Outgoing replies ────────────────────────────────────────────────
    log("Outgoing replies")
    ensure_record("out_parking", "eo_outgoing_reply", {
        "subject_ar": "رد الأمانة على خطاب توحيد تعرفة المواقف",
        "subject_en": "Reply on unifying public parking tariffs",
        "priority": "high", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "addressee": D["momrah"], "approved_by": me, "sent_on": "2026-10-12",
        "channel": "admin_communications_system",
        "reply_text": "إشارة إلى خطابكم رقم MOMRAH-2026-14420، تفيد الأمانة بموافقتها على توحيد تعرفة "
                      "مواقف السيارات في النطاق المركزي على ثلاث مراحل تبدأ في الربع الأول من ٢٠٢٧، "
                      "مع إعفاء ساعة الوقوف الأولى، وتعديل المادة الرابعة من اللائحة قبل التطبيق.",
        "delegation_note": "صدر بتفويض وكيل الأمانة للخدمات",
    }, parent="letter_parking", link_key="eo_outgoing_reply_parent")

    ensure_record("out_school", "eo_outgoing_reply", {
        "subject_ar": "رد الأمانة على خطاب السلامة المرورية في محيط المدارس",
        "subject_en": "Reply on road safety around schools",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "addressee": D["traffic"], "approved_by": me, "sent_on": "2026-11-12",
        "channel": "official_email",
        "reply_text": "إلحاقاً بخطابكم رقم GDT-2026-7781، نفيدكم بحصر ستة تقاطعات في محيط المدارس، "
                      "وبالبدء في معالجة أربعة منها ضمن عقد الصيانة القائم، فيما أدرج تقاطعان "
                      "ضمن مشروع الإشارات الضوئية للعام القادم.",
    }, parent="letter_school", link_key="eo_outgoing_reply_parent")

    ensure_record("out_food", "eo_outgoing_reply", {
        "subject_ar": "رد الأمانة على خطاب الرقابة على المنشآت الغذائية",
        "subject_en": "Reply on inspection of food establishments",
        "priority": "normal", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "addressee": D["health"], "approved_by": me, "sent_on": "2026-11-26",
        "channel": "official_email",
        "reply_text": "مشروع رد يوضح إعادة توزيع جداول فرق الرقابة لرفع عدد الزيارات التفتيشية بنسبة ٣٠٪ "
                      "اعتباراً من ديسمبر، وآلية التقرير الشهري.",
    }, parent="letter_food", link_key="eo_outgoing_reply_parent")

    # ── 4. Directives ──────────────────────────────────────────────────────
    log("Directives")
    ensure_record("dir_permits", "eo_directive", {
        "subject_ar": "خفض زمن إصدار رخص البناء إلى خمسة أيام عمل",
        "subject_en": "Cut building-permit issuance time to five working days",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2027-03-31",
        "directive_origin": "weekly_meeting", "origin_date": "2026-09-14",
        "decision_reference": "محضر الاجتماع الأسبوعي رقم ٣٧",
        "directive_text": "خفض متوسط زمن إصدار رخصة البناء السكنية من اثني عشر يوم عمل إلى خمسة أيام عمل "
                          "بنهاية الربع الأول من عام ٢٠٢٧.",
        "required_outcome": "متوسط زمن إصدار لا يتجاوز خمسة أيام عمل لرخص البناء السكنية، "
                            "ونسبة إعادة الطلبات أقل من ١٠٪.",
        "responsible_officer": me,
        "expected_deliverable": "خطة تنفيذية معتمدة، ولوحة متابعة أسبوعية لزمن الإصدار، وتقرير إغلاق.",
        "plan_required": True,
    })

    ensure_record("dir_rain", "eo_directive", {
        "subject_ar": "رفع جاهزية تصريف مياه الأمطار استجابة لخطاب الإمارة",
        "subject_en": "Raise storm-water drainage readiness in response to the Emirate letter",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2026-12-05",
        "directive_origin": "correspondence", "origin_date": "2026-10-29",
        "origin_record": RECORDS["letter_rain"]["id"],
        "directive_text": "رفع جاهزية شبكات تصريف مياه الأمطار إلى ١٠٠٪ في القطاعات الخمسة، "
                          "واعتماد خطة الاستجابة الميدانية قبل الخامس من ديسمبر.",
        "required_outcome": "تنظيف كامل لنقاط التصريف البالغة ٧٨٠ نقطة، وجاهزية ١٢ فرقة استجابة، "
                            "وتقرير موحد مرفوع للإمارة.",
        "responsible_officer": me,
        "expected_deliverable": "تقرير جاهزية موحد وخطة استجابة معتمدة.",
        "plan_required": True,
    })

    ensure_record("dir_parking", "eo_directive", {
        "subject_ar": "تهيئة تطبيق تعرفة المواقف الموحدة",
        "subject_en": "Prepare for the unified parking tariff rollout",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2026-11-15",
        "directive_origin": "weekly_meeting", "origin_date": "2026-10-05",
        "decision_reference": "محضر الاجتماع الأسبوعي رقم ٤٠",
        "directive_text": "استكمال متطلبات تطبيق التعرفة الموحدة: تعديل المادة الرابعة من اللائحة، "
                          "وتحديث اللوحات الإرشادية في النطاق المركزي.",
        "required_outcome": "مسودة تعديل اللائحة مرفوعة للدراسة، وجرد كامل للوحات الإرشادية المطلوب تحديثها.",
        "responsible_officer": me,
        "expected_deliverable": "مسودة التعديل وتقرير جرد اللوحات.",
        "plan_required": False,
    })

    ensure_record("dir_lighting", "eo_directive", {
        "subject_ar": "رفع كفاءة الإنارة في الشوارع الرئيسة",
        "subject_en": "Improve street-lighting efficiency on main roads",
        "priority": "normal", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"], "assigned_user_id": me, "due_date": "2027-06-30",
        "directive_origin": "committee", "origin_date": "2026-11-09",
        "decision_reference": "توصية لجنة الخدمات رقم ١٢",
        "directive_text": "استبدال وحدات الإنارة التقليدية في الشوارع الرئيسة بوحدات موفرة للطاقة "
                          "وفق أولوية الاستهلاك.",
        "required_outcome": "خفض استهلاك الإنارة بنسبة ٢٥٪ في الشوارع المستهدفة.",
        "responsible_officer": me,
        "expected_deliverable": "حصر الوحدات، وخطة استبدال مرحلية، وتقرير وفر الاستهلاك.",
        "plan_required": True,
    })

    # ── 5. Execution plans ─────────────────────────────────────────────────
    log("Execution plans")
    ensure_record("plan_permits_v1", "eo_execution_plan", {
        "subject_ar": "الخطة التنفيذية الأولى لخفض زمن إصدار الرخص",
        "subject_en": "First execution plan for permit issuance time",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "plan_summary": "خطة من مرحلتين: حصر خطوات المسار الحالي، ثم إلغاء الخطوات المكررة. "
                        "أغلقت بعد اعتماد نسخة موسعة تشمل الربط الإلكتروني.",
        "plan_start": "2026-09-20", "plan_end": "2026-10-31",
        "approved_by": me, "approval_date": "2026-09-20",
    }, parent="dir_permits", link_key="eo_execution_plan_parent")

    ensure_record("plan_permits_v2", "eo_execution_plan", {
        "subject_ar": "الخطة التنفيذية المعتمدة لخفض زمن إصدار الرخص",
        "subject_en": "Approved execution plan for permit issuance time",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "plan_summary": "خطة من ثلاث مراحل: إعادة هندسة المسار، والربط الإلكتروني مع الجهات المانحة "
                        "للموافقات، ثم التشغيل التجريبي في بلديتين قبل التعميم.",
        "plan_start": "2026-11-01", "plan_end": "2027-03-15",
        "approved_by": me, "approval_date": "2026-10-29",
        "supersedes": RECORDS["plan_permits_v1"]["id"],
    }, parent="dir_permits", link_key="eo_execution_plan_parent")

    ensure_record("plan_rain", "eo_execution_plan", {
        "subject_ar": "خطة رفع جاهزية تصريف مياه الأمطار",
        "subject_en": "Execution plan for storm-water drainage readiness",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "plan_summary": "خطة من مرحلتين قبل موسم الشتاء: استكمال تنظيف نقاط التصريف، "
                        "ثم تجهيز فرق الاستجابة ومعداتها وتوزيعها على القطاعات.",
        "plan_start": "2026-11-01", "plan_end": "2026-12-05",
        "approved_by": me, "approval_date": "2026-11-01",
    }, parent="dir_rain", link_key="eo_execution_plan_parent")

    # ── 6. Execution plan stages ───────────────────────────────────────────
    log("Execution plan stages")
    ensure_record("stage_v1_s1", "eo_execution_plan_stage", {
        "subject_ar": "المرحلة الأولى: حصر خطوات المسار الحالي",
        "subject_en": "Stage 1: map the current permit workflow",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "stage": "حصر خطوات المسار الحالي", "weeks": 3, "body": D["planning"],
        "output": "خريطة مسار موثقة تبين ٢٣ خطوة وزمن كل خطوة.",
    }, parent="plan_permits_v1", link_key="eo_execution_plan_stage_parent")

    ensure_record("stage_v1_s2", "eo_execution_plan_stage", {
        "subject_ar": "المرحلة الثانية: إلغاء الخطوات المكررة",
        "subject_en": "Stage 2: remove duplicated steps",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "stage": "إلغاء الخطوات المكررة", "weeks": 3, "body": D["planning"],
        "output": "قرار إداري بإلغاء سبع خطوات مكررة من المسار.",
    }, parent="plan_permits_v1", link_key="eo_execution_plan_stage_parent")

    ensure_record("stage_v2_s1", "eo_execution_plan_stage", {
        "subject_ar": "المرحلة الأولى: إعادة هندسة مسار الرخصة",
        "subject_en": "Stage 1: re-engineer the permit workflow",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "stage": "إعادة هندسة مسار الرخصة", "weeks": 4, "body": D["planning"],
        "output": "مسار معتمد من تسع خطوات مع اتفاقية مستوى خدمة لكل خطوة.",
    }, parent="plan_permits_v2", link_key="eo_execution_plan_stage_parent")

    ensure_record("stage_v2_s2", "eo_execution_plan_stage", {
        "subject_ar": "المرحلة الثانية: الربط الإلكتروني مع الجهات المانحة للموافقات",
        "subject_en": "Stage 2: electronic integration with approving authorities",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "stage": "الربط الإلكتروني مع الجهات المانحة للموافقات", "weeks": 6, "body": D["roads"],
        "output": "ربط تشغيلي مع ثلاث جهات يلغي الموافقات الورقية.",
    }, parent="plan_permits_v2", link_key="eo_execution_plan_stage_parent")

    ensure_record("stage_v2_s3", "eo_execution_plan_stage", {
        "subject_ar": "المرحلة الثالثة: التشغيل التجريبي في بلديتين",
        "subject_en": "Stage 3: pilot operation in two municipalities",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "stage": "التشغيل التجريبي في بلديتين", "weeks": 8, "body": D["services"],
        "output": "تقرير تشغيل تجريبي يوصي بالتعميم أو بالتعديل.",
    }, parent="plan_permits_v2", link_key="eo_execution_plan_stage_parent")

    ensure_record("stage_rain_s1", "eo_execution_plan_stage", {
        "subject_ar": "المرحلة الأولى: استكمال تنظيف نقاط التصريف",
        "subject_en": "Stage 1: finish cleaning the drainage points",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "stage": "استكمال تنظيف نقاط التصريف", "weeks": 3, "body": D["services"],
        "output": "تنظيف ١٤٠ نقطة متبقية وتوثيقها بالصور الميدانية.",
    }, parent="plan_rain", link_key="eo_execution_plan_stage_parent")

    ensure_record("stage_rain_s2", "eo_execution_plan_stage", {
        "subject_ar": "المرحلة الثانية: تجهيز فرق الاستجابة وتوزيعها",
        "subject_en": "Stage 2: equip and deploy the response teams",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"],
        "stage": "تجهيز فرق الاستجابة وتوزيعها", "weeks": 2, "body": D["services"],
        "output": "١٢ فرقة مجهزة بالمضخات وموزعة على خمسة قطاعات.",
    }, parent="plan_rain", link_key="eo_execution_plan_stage_parent")

    # ── 7. Assignments ─────────────────────────────────────────────────────
    # NOTE: an assignment's reference pattern is EO-DIR-YYYY-NNN-A#, which
    # expects a FOUR-segment parent reference, so its parent can only be a
    # DIRECTIVE — never an execution plan, although the link type allows one.
    # The tie to a stage is carried by the `from_stage` field instead.
    log("Assignments")
    ensure_record("asg_permits_planning", "eo_assignment", {
        "subject_ar": "تكليف: إعادة هندسة مسار الرخصة",
        "subject_en": "Assignment: re-engineer the permit workflow",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "due_date": "2026-12-15",
        "assigned_body": D["planning"], "assignment_owner": me,
        "scope": "إعادة تصميم خطوات إصدار الرخصة السكنية بما لا يتجاوز تسع خطوات، "
                 "واعتماد اتفاقية مستوى خدمة لكل خطوة.",
        "from_stage": RECORDS["stage_v2_s1"]["id"],
        "start_date": "2026-11-02", "completion_rate": 60,
        "evidence_note": "سلمت المسودة الأولى للمسار الجديد واعتمدت في اجتماع ١٨ نوفمبر.",
    }, parent="dir_permits", link_key="eo_assignment_parent")

    ensure_record("asg_permits_survey", "eo_assignment", {
        "subject_ar": "تكليف: حصر خطوات المسار الحالي وزمنها",
        "subject_en": "Assignment: survey the current workflow and its timings",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "due_date": "2026-10-20",
        "assigned_body": D["planning"], "assignment_owner": me,
        "scope": "قياس زمن كل خطوة في المسار الحالي على عينة من ٢٠٠ طلب، وتحديد الخطوات المكررة.",
        "from_stage": RECORDS["stage_v1_s1"]["id"],
        "start_date": "2026-09-21", "completion_rate": 100,
        "evidence_note": "سلم تقرير القياس متضمناً ٢٣ خطوة وسبع خطوات مكررة.",
    }, parent="dir_permits", link_key="eo_assignment_parent")

    ensure_record("asg_rain_services", "eo_assignment", {
        "subject_ar": "تكليف: استكمال تنظيف نقاط التصريف",
        "subject_en": "Assignment: finish cleaning the drainage points",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"], "due_date": "2026-11-28",
        "assigned_body": D["services"], "assignment_owner": me,
        "scope": "تنظيف ١٤٠ نقطة تصريف متبقية في القطاعين الشمالي والشرقي وتوثيقها.",
        "from_stage": RECORDS["stage_rain_s1"]["id"],
        "start_date": "2026-11-03", "completion_rate": 35,
    }, parent="dir_rain", link_key="eo_assignment_parent")

    ensure_record("asg_parking_legal", "eo_assignment", {
        "subject_ar": "تكليف: إعداد مسودة تعديل المادة الرابعة",
        "subject_en": "Assignment: draft the amendment to article four",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "due_date": "2026-11-10",
        "assigned_body": D["legal"], "assignment_owner": me,
        "scope": "إعداد مسودة تعديل المادة الرابعة من لائحة المواقف ورفعها لدراسة مشروع القرار.",
        "start_date": "2026-10-06", "completion_rate": 100,
        "evidence_note": "رفعت المسودة ضمن مشروع القرار DRF المتعلق بتعرفة المواقف.",
    }, parent="dir_parking", link_key="eo_assignment_parent")

    # ── 8. Tasks ───────────────────────────────────────────────────────────
    log("Tasks")
    ensure_record("task_parking_signs", "eo_task", {
        "subject_ar": "جرد اللوحات الإرشادية في النطاق المركزي",
        "subject_en": "Inventory the guidance signage in the central district",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "due_date": "2026-11-09",
        "task_text": "حصر اللوحات الإرشادية للمواقف في النطاق المركزي وتحديد ما يلزم تحديثه قبل التطبيق.",
        "owner": me, "start_date": "2026-10-07", "completion_rate": 100,
    }, parent="dir_parking", link_key="eo_task_parent")

    ensure_record("task_parking_impact", "eo_task", {
        "subject_ar": "إعداد تقدير الأثر المالي للتعرفة الموحدة",
        "subject_en": "Estimate the financial impact of the unified tariff",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "due_date": "2026-11-12",
        "task_text": "تقدير الإيراد المتوقع والأثر على المستفيدين لكل مرحلة من مراحل التطبيق الثلاث.",
        "owner": me, "start_date": "2026-10-08", "completion_rate": 100,
    }, parent="dir_parking", link_key="eo_task_parent")

    ensure_record("task_permits_dashboard", "eo_task", {
        "subject_ar": "تجهيز لوحة متابعة أسبوعية لزمن الإصدار",
        "subject_en": "Build a weekly dashboard for issuance time",
        "priority": "normal", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "due_date": "2026-12-20",
        "task_text": "تجهيز لوحة متابعة أسبوعية تعرض متوسط زمن الإصدار ونسبة إعادة الطلبات لكل بلدية.",
        "owner": me, "start_date": "2026-11-16", "completion_rate": 40,
    }, parent="dir_permits", link_key="eo_task_parent")

    ensure_record("task_lighting_survey", "eo_task", {
        "subject_ar": "حصر وحدات الإنارة في الشوارع الرئيسة",
        "subject_en": "Count the lighting units on main roads",
        "priority": "normal", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"], "due_date": "2027-01-15",
        "task_text": "حصر وحدات الإنارة التقليدية في الشوارع الرئيسة وتصنيفها حسب الاستهلاك.",
        "owner": me,
    }, parent="dir_lighting", link_key="eo_task_parent")

    ensure_record("task_rain_consolidate", "eo_task", {
        "subject_ar": "إعداد التقرير الموحد لجاهزية التصريف",
        "subject_en": "Compile the consolidated drainage readiness report",
        "priority": "urgent", "sensitivity": "internal", "source": "correspondence",
        "owning_body_id": D["eo"], "due_date": "2026-12-02",
        "task_text": "دمج ردي وكالة الخدمات البلدية ووكالة الطرق والنقل في تقرير موحد يرفع للإمارة.",
        "owner": me, "start_date": "2026-11-20", "completion_rate": 55,
    }, parent="letter_rain", link_key="eo_task_parent")

    # ── 9. Draft decision studies ──────────────────────────────────────────
    log("Draft decision studies")
    # The study behind the tariff decision that is now superseded. It exists
    # because a Decision Record's `issue` transition carries a close_parent
    # action: a decision with no draft-study parent CANNOT be issued (the
    # engine refuses rather than leaving an unclosable parent), so a standing
    # decision needs the study it came out of.
    ensure_record("drf_parking_2026h", "eo_draft_decision_study", {
        "subject_ar": "مشروع قرار تعرفة مواقف السيارات المرحلية",
        "subject_en": "Draft decision on the interim parking tariff",
        "priority": "normal", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"], "due_date": "2026-02-20",
        "proposer": me, "proposing_body": D["roads"], "claimed_urgency": "normal",
        "grounds": "توحيد تعرفة المواقف مرحلياً لحين اكتمال دراسة النطاق المركزي.",
        "draft_text": "تحدد تعرفة مواقف السيارات بريال واحد لكل ساعة في جميع النطاقات "
                      "اعتباراً من ١ مارس ٢٠٢٦.",
        "conflict_verdict": "no_conflict", "feasibility": "feasible",
        "feasibility_notes": "قابل للتطبيق بالأجهزة القائمة دون تكلفة إضافية.",
        "issuing_authority": "أمين المنطقة",
        "affected_bodies": [D["roads"], D["legal"]],
        "recommendation": "approve_as_proposed",
    })

    ensure_record("drf_parking", "eo_draft_decision_study", {
        "subject_ar": "مشروع قرار تعديل تعرفة مواقف السيارات في النطاق المركزي",
        "subject_en": "Draft decision amending the central-district parking tariff",
        "priority": "high", "sensitivity": "internal", "source": "weekly_meeting",
        "owning_body_id": D["eo"], "due_date": "2026-11-18",
        "proposer": me, "proposing_body": D["roads"], "claimed_urgency": "urgent",
        "grounds": "تنفيذاً لخطاب الوزارة رقم MOMRAH-2026-14420 ولتوحيد التعرفة بين النطاقات.",
        "draft_text": "تعدل المادة الرابعة من لائحة مواقف السيارات لتصبح التعرفة ريالين لكل ساعة "
                      "في النطاق المركزي، وتطبق على ثلاث مراحل اعتباراً من ١ يناير ٢٠٢٧.",
        "conflict_verdict": "consistent", "feasibility": "feasible_with_conditions",
        "feasibility_notes": "قابل للتطبيق بشرط تحديث اللوحات الإرشادية وتهيئة أجهزة الدفع قبل المرحلة الأولى.",
        "issuing_authority": "أمين المنطقة",
        "affected_bodies": [D["roads"], D["legal"], D["services"]],
        "recommendation": "approve_as_redrafted",
        "redraft_text": "تعدل الصياغة بإعفاء ساعة الوقوف الأولى، وبتأجيل تطبيق المرحلة الثالثة "
                        "إلى الربع الثالث من ٢٠٢٧، وبإضافة مراجعة أثر بعد ستة أشهر.",
    })

    ensure_record("drf_foodtrucks", "eo_draft_decision_study", {
        "subject_ar": "مشروع قرار تنظيم عربات الأغذية المتنقلة",
        "subject_en": "Draft decision regulating mobile food trucks",
        "priority": "normal", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"], "due_date": "2026-11-10",
        "proposer": me, "proposing_body": D["services"], "claimed_urgency": "normal",
        "grounds": "تنظيم نشاط عربات الأغذية المتنقلة بعد ارتفاع عدد الطلبات إلى ٣٤٠ طلباً خلال عام.",
        "draft_text": "يشترط لترخيص عربة الأغذية المتنقلة موقع معتمد ضمن المواقع المحددة، "
                      "وشهادة سلامة غذائية سارية، والتزام بساعات التشغيل المحددة.",
        "conflict_verdict": "no_conflict", "feasibility": "feasible",
        "feasibility_notes": "لا يتطلب موارد إضافية؛ يدار ضمن منظومة التراخيص القائمة.",
        "issuing_authority": "وكيل الأمانة للخدمات",
        "affected_bodies": [D["services"], D["legal"]],
        "recommendation": "approve_as_proposed",
    })

    ensure_record("drf_markets", "eo_draft_decision_study", {
        "subject_ar": "مشروع قرار مواعيد تشغيل الأسواق الشعبية",
        "subject_en": "Draft decision on operating hours of popular markets",
        "priority": "normal", "sensitivity": "internal", "source": "direct_instruction",
        "owning_body_id": D["eo"], "due_date": "2026-12-08",
        "proposer": me, "proposing_body": D["services"], "claimed_urgency": "normal",
        "grounds": "شكاوى متكررة من سكان الأحياء المجاورة بشأن التشغيل بعد منتصف الليل.",
        "draft_text": "تحدد مواعيد تشغيل الأسواق الشعبية من السادسة صباحاً حتى الحادية عشرة مساءً.",
        "conflict_verdict": "conflicts", "feasibility": "feasible_with_conditions",
        "feasibility_notes": "يتعارض جزئياً مع قرار تشغيل الأسواق الموسمية، ويحتاج استثناءً لرمضان.",
        "issuing_authority": "وكيل الأمانة للخدمات",
        "affected_bodies": [D["services"], D["legal"], D["planning"]],
        "recommendation": "return_for_clarification",
    })

    ensure_record("drf_sidewalks", "eo_draft_decision_study", {
        "subject_ar": "مشروع قرار رسوم إشغال الأرصفة للمقاهي",
        "subject_en": "Draft decision on sidewalk occupancy fees for cafés",
        "priority": "normal", "sensitivity": "confidential", "source": "committee",
        "owning_body_id": D["eo"], "due_date": "2026-12-20",
        "proposer": me, "proposing_body": D["planning"], "claimed_urgency": "normal",
        "grounds": "مقترح لتنظيم إشغال الأرصفة أمام المقاهي وتحصيل رسوم سنوية عنه.",
        "draft_text": "تفرض رسوم سنوية قدرها ٣٠٠ ريال للمتر المربع على إشغال الرصيف أمام المنشأة، "
                      "بحد أقصى ٥٠٪ من عرض الرصيف.",
        "conflict_verdict": "duplicates", "feasibility": "not_feasible",
        "feasibility_notes": "يكرر رسوماً مفروضة في لائحة الإشغال المؤقت، ويخفض عرض الرصيف "
                             "دون الحد النظامي للمشاة.",
        "issuing_authority": "أمين المنطقة",
        "affected_bodies": [D["planning"], D["legal"]],
        "recommendation": "reject",
    })

    # ── 10. Objections ─────────────────────────────────────────────────────
    log("Objections")
    ensure_record("obj_parking_article4", "eo_objection", {
        "subject_ar": "اعتراض على صياغة المادة الرابعة المعدلة",
        "subject_en": "Objection to the wording of the amended article four",
        "priority": "high", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"],
        "clause_ref": "المادة الرابعة / الفقرة ٢",
        "objection_reason": "الصياغة لا تستثني ساعة الوقوف الأولى، وهو ما اتفق عليه في الموقف الموحد "
                            "المرفوع للوزارة، وتطبيقها كما وردت يخالف ما أبلغت به الوزارة.",
        "raised_by": me, "outcome": "accepted",
        "outcome_reason": "قبل الاعتراض وأعيدت الصياغة بإضافة إعفاء ساعة الوقوف الأولى.",
    }, parent="drf_parking", link_key="eo_objection_parent")

    ensure_record("obj_parking_phase3", "eo_objection", {
        "subject_ar": "اعتراض على موعد تطبيق المرحلة الثالثة",
        "subject_en": "Objection to the timing of the third rollout phase",
        "priority": "normal", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"],
        "clause_ref": "جدول التطبيق / المرحلة ٣",
        "objection_reason": "تطبيق المرحلة الثالثة في الربع الثاني يتزامن مع أعمال سفلتة النطاق المركزي.",
        "raised_by": me, "outcome": "accepted",
        "outcome_reason": "قبل الاعتراض وأجلت المرحلة الثالثة إلى الربع الثالث من ٢٠٢٧.",
    }, parent="drf_parking", link_key="eo_objection_parent")

    ensure_record("obj_foodtrucks_hours", "eo_objection", {
        "subject_ar": "اعتراض على تقييد ساعات تشغيل العربات",
        "subject_en": "Objection to restricting food-truck operating hours",
        "priority": "normal", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"],
        "clause_ref": "المادة الثالثة / ساعات التشغيل",
        "objection_reason": "تقييد التشغيل حتى منتصف الليل يحرم العربات من ذروة نهاية الأسبوع.",
        "raised_by": me, "outcome": "dismissed",
        "outcome_reason": "رفض الاعتراض: ساعات التشغيل مطابقة لما هو مقرر للمنشآت الغذائية الثابتة.",
    }, parent="drf_foodtrucks", link_key="eo_objection_parent")

    ensure_record("obj_markets_ramadan", "eo_objection", {
        "subject_ar": "اعتراض على عدم استثناء شهر رمضان",
        "subject_en": "Objection to the absence of a Ramadan exemption",
        "priority": "normal", "sensitivity": "internal", "source": "committee",
        "owning_body_id": D["eo"],
        "clause_ref": "المادة الأولى / مواعيد التشغيل",
        "objection_reason": "المسودة لا تستثني شهر رمضان، وهو ما يعطل أسواق التمور والمستلزمات الموسمية.",
        "raised_by": me, "outcome": "accepted",
        "outcome_reason": "قيد النظر ضمن إعادة الدراسة: يرجح إضافة استثناء موسمي لشهر رمضان.",
    }, parent="drf_markets", link_key="eo_objection_parent")

    # ── 11. Decision records ───────────────────────────────────────────────
    log("Decision records")
    ensure_record("dec_parking_old", "eo_decision_record", {
        "subject_ar": "قرار تعرفة مواقف السيارات المرحلية",
        "subject_en": "Decision on the interim parking tariff",
        "priority": "normal", "sensitivity": "public", "source": "committee",
        "owning_body_id": D["eo"],
        "decision_text": "تحدد تعرفة مواقف السيارات بريال واحد لكل ساعة في جميع النطاقات "
                         "اعتباراً من ١ مارس ٢٠٢٦.",
        "issued_by": me, "effective_date": "2026-03-01",
        "keywords": ["parking", "tariffs"], "standing": "superseded",
    }, parent="drf_parking_2026h", link_key="eo_decision_record_parent")

    ensure_record("dec_parking_new", "eo_decision_record", {
        "subject_ar": "قرار تعرفة مواقف السيارات في النطاق المركزي",
        "subject_en": "Decision on the central-district parking tariff",
        "priority": "high", "sensitivity": "public", "source": "weekly_meeting",
        "owning_body_id": D["eo"],
        "decision_text": "تعدل المادة الرابعة من لائحة مواقف السيارات لتصبح التعرفة ريالين لكل ساعة "
                         "في النطاق المركزي مع إعفاء ساعة الوقوف الأولى، وتطبق على ثلاث مراحل "
                         "تبدأ في ١ يناير ٢٠٢٧ وتؤجل المرحلة الثالثة إلى الربع الثالث من ٢٠٢٧.",
        "issued_by": me, "effective_date": "2027-01-01",
        "keywords": ["parking", "tariffs", "central_district"], "standing": "in_force",
        "supersedes": RECORDS["dec_parking_old"]["id"],
    }, parent="drf_parking", link_key="eo_decision_record_parent")

    ensure_record("dec_foodtrucks", "eo_decision_record", {
        "subject_ar": "قرار تنظيم عربات الأغذية المتنقلة",
        "subject_en": "Decision regulating mobile food trucks",
        "priority": "normal", "sensitivity": "public", "source": "committee",
        "owning_body_id": D["eo"],
        "decision_text": "يشترط لترخيص عربة الأغذية المتنقلة موقع معتمد وشهادة سلامة غذائية سارية "
                         "والتزام بساعات التشغيل المحددة، ويعمل به بعد ثلاثين يوماً من النشر.",
        "issued_by": me, "effective_date": "2026-12-15",
        "keywords": ["food_trucks", "licensing"], "standing": "in_force",
    }, parent="drf_foodtrucks", link_key="eo_decision_record_parent")

    # ── workflows ──────────────────────────────────────────────────────────
    # Children that BLOCK a parent's closure (referrals, execution plans,
    # assignments, tasks) are moved first, so the order below is the order the
    # engine demands, not a preference.
    log("Transitions")

    run("ref_parking_roads", ["record_response"], "ورد رد وكالة الطرق والنقل متضمناً الموافقة على التدرج.")
    run("ref_parking_legal", ["record_response"], "ورد الرأي القانوني: التعديل يقتصر على المادة الرابعة.")
    run("ref_school_roads", ["record_response"], "وردت دراسة التقاطعات الستة مع التكلفة التقديرية.")
    run("ref_rain_services", ["record_response"], "ورد تقرير الجاهزية: ٨٢٪ ونسبة التنظيف ٦٤٠ من ٧٨٠.")
    # ref_rain_roads is left in `sent`: a branch still running against its
    # derived three-day deadline, which is why its letter cannot close.

    run("letter_parking", ["analyse", "brief", "direct", "consolidate", "close"],
        "أغلق الخطاب بعد رفع الموقف الموحد وإرسال الرد للوزارة.")
    run("letter_school", ["analyse", "brief", "direct"],
        "وجه الخطاب إلى وكالة الطرق والنقل بعد اعتماد الموجز.")
    run("letter_rain", ["analyse", "brief", "direct", "consolidate"],
        "بدأ تجميع الردود؛ ما زال رد وكالة الطرق والنقل متوقعاً.")
    run("letter_food", ["analyse", "brief"], "اعتمد الموجز والتوصية بالمعالجة داخل المكتب.")
    # letter_report stays in `received`: it arrived on 18 November and is
    # still on the desk.

    run("out_parking", ["approve", "send"], "أرسل الرد عبر نظام الاتصالات الإدارية برقم صادر معتمد.")
    run("out_school", ["approve"], "اعتمد الرد وينتظر الإرسال عبر البريد الرسمي.")
    # out_food stays in `drafted`.

    run("obj_parking_article4", ["resolve"], "قبل الاعتراض وعدلت الصياغة.")
    run("obj_parking_phase3", ["resolve"], "قبل الاعتراض وأجلت المرحلة الثالثة.")
    run("obj_foodtrucks_hours", ["resolve"], "رفض الاعتراض لمطابقة الساعات للمنشآت الثابتة.")
    # obj_markets_ramadan stays in `raised` — and because an open objection
    # blocks `raise_to_he`, drf_markets cannot go to H.E. while it stands.

    run("drf_parking_2026h", ["submit", "begin_study", "raise_to_he", "close"],
        "أغلقت الدراسة بموافقة المعالي على التعرفة المرحلية.",
        values={"close": {
            "he_decision": "approved_original",
            "he_reason": "الموافقة على التعرفة المرحلية كما وردت لحين اكتمال دراسة النطاق المركزي.",
            "decision_date": "2026-02-18",
        }})
    run("drf_parking", ["submit", "begin_study", "raise_to_he", "close"],
        "أغلقت الدراسة بموافقة المعالي على الصياغة المعدلة.",
        values={"close": {
            "he_decision": "approved_redrafted",
            "he_reason": "الموافقة على الصياغة المعدلة بعد قبول الاعتراضين على المادة الرابعة وجدول التطبيق.",
            "decision_date": "2026-11-19",
        }})
    run("drf_foodtrucks", ["submit", "begin_study", "raise_to_he", "close"],
        "أغلقت الدراسة بموافقة المعالي على المشروع كما ورد.",
        values={"close": {
            "he_decision": "approved_original",
            "he_reason": "الموافقة على المشروع كما ورد؛ الاعتراض على ساعات التشغيل غير وارد.",
            "decision_date": "2026-11-12",
        }})
    run("drf_markets", ["submit", "begin_study"],
        "بدأت الدراسة؛ الاعتراض على استثناء رمضان ما زال قائماً.")
    # drf_sidewalks stays in `received`.

    # Issuing a decision runs a close_parent action on its study. Each study
    # above is already closed by then, so the action reports itself skipped
    # instead of trying to close it twice — which is why these run last.
    run("dec_parking_old", ["issue"], "صدر القرار ونشر في ١ مارس ٢٠٢٦.")
    run("dec_parking_new", ["issue"], "صدر القرار بعد موافقة المعالي على الصياغة المعدلة.")
    # dec_foodtrucks stays in `drafted` pending publication.

    run("stage_v1_s1", ["close"], "أقفلت المرحلة بتسليم خريطة المسار.")
    run("stage_v1_s2", ["close"], "أقفلت المرحلة بصدور قرار إلغاء الخطوات المكررة.")
    run("stage_v2_s1", ["close"], "أقفلت المرحلة باعتماد المسار الجديد من تسع خطوات.")
    # stage_v2_s2, stage_v2_s3, stage_rain_s1, stage_rain_s2 stay `open`.

    run("asg_permits_survey", ["start", "submit_evidence", "verify_and_close"],
        "اعتمد تقرير القياس وأقفل التكليف.",
        values={"verify_and_close": {"verified_by": me, "verified_on": "2026-10-22"}})
    run("asg_permits_planning", ["start"], "بوشر العمل على إعادة هندسة المسار.")
    run("asg_parking_legal", ["start", "submit_evidence", "verify_and_close"],
        "اعتمدت مسودة التعديل وأقفل التكليف.",
        values={"verify_and_close": {"verified_by": me, "verified_on": "2026-11-08"}})
    # asg_rain_services stays in `assigned`.

    run("task_parking_signs", ["start", "verify_and_close"],
        "سلم جرد اللوحات واعتمد.", values={"verify_and_close": {"verified_by": me}})
    run("task_parking_impact", ["start", "verify_and_close"],
        "سلم تقدير الأثر المالي واعتمد.", values={"verify_and_close": {"verified_by": me}})
    run("task_permits_dashboard", ["start"], "بوشر تجهيز لوحة المتابعة.")
    run("task_rain_consolidate", ["start"], "بوشر إعداد التقرير الموحد.")
    # task_lighting_survey stays `open`.

    # The first plan is closed because a wider plan replaced it; the second is
    # approved and running.
    run("plan_permits_v1", ["approve", "close"], "أقفلت الخطة بعد اعتماد نسخة موسعة تحل محلها.")
    run("plan_permits_v2", ["approve"], "اعتمدت الخطة الموسعة وبدأ تنفيذها.")
    # plan_rain stays in `drafting`.

    run("dir_parking", ["assign_directly", "submit_for_verification", "close"],
        "أغلق التوجيه بعد استكمال المسودة وجرد اللوحات.",
        values={"close": {
            "verification_notes": "تحقق المكتب من تسليم مسودة تعديل المادة الرابعة ومن اكتمال جرد "
                                  "اللوحات الإرشادية البالغة ٤١٢ لوحة.",
        }})
    run("dir_permits", ["require_plan", "plan_approved"], "اعتمدت الخطة الموسعة وانتقل التوجيه للتنفيذ.")
    run("dir_rain", ["require_plan"], "التوجيه في مرحلة إعداد الخطة التنفيذية.")
    # dir_lighting stays in `recorded`.

    # ── summary ────────────────────────────────────────────────────────────
    print()
    counts, states = {}, {}
    key_of = {t["id"]: t["key"] for t in TYPES.values()}
    for r in list_all_records():
        k = key_of[r["record_type_id"]]
        counts[k] = counts.get(k, 0) + 1
        states.setdefault(k, {})
        sc = STATE_CODE.get(r.get("state_id"), "?")
        states[k][sc] = states[k].get(sc, 0) + 1
    total = 0
    for k in sorted(counts):
        total += counts[k]
        spread = ", ".join(f"{s}×{n}" for s, n in sorted(states[k].items()))
        print(f"{k:30} {counts[k]:3}   {spread}")
    print(f"{'TOTAL':30} {total:3}")


if __name__ == "__main__":
    try:
        main()
    except ApiError as e:
        print(f"\nFAILED: {e}", file=sys.stderr)
        sys.exit(1)
