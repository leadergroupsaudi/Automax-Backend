// seed_eo_arabic.go applies the client's 2026-11-27 Arabic sign-off onto the
// EO catalogue. It is a CONVERGENT seed step: on every boot it re-reads the
// register of English labels still placeholding for Arabic (label_ar == label,
// name_ar == name, or label_ar == label on a dropdown option) and fills each
// from the map below. A label an administrator has since edited stays — the
// step only overwrites a row whose Arabic still equals its English.
//
// The map is the Excel in the project root (Arabic Translation.xlsx) frozen
// into source: 206 EN→AR pairs covering record type names, workflow state
// names, workflow transition names, field labels, and dropdown option labels.
// Fresh installs pick it up automatically; existing databases pick it up on
// next boot. See internal/database/seed_eo_pilot.go for the step list.

package database

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

const eoApplyArabicLabelsStepKey = "apply_arabic_labels_2026_11_27"


var eoArabicLabels = map[string]string{
	"Accepted": "مقبول",
	"Addressee": "الجهة المرسَل إليها",
	"Administrative communications system": "نظام الاتصالات الإدارية",
	"Affected bodies": "الجهات المعنية",
	"Amended by H.E.": "عدّله معاليه",
	"Analyse the letter": "تحليل الخطاب",
	"Answered": "تم الرد",
	"Approval date": "تاريخ الاعتماد",
	"Approve as proposed": "الاعتماد كما ورد",
	"Approve as redrafted": "الاعتماد بالصياغة المعدلة",
	"Approve the plan": "اعتماد الخطة",
	"Approve the reply": "اعتماد الرد",
	"Approved": "معتمد",
	"Approved by": "اعتمدها / اعتمده",
	"Approved original": "اعتُمد النص الأصلي",
	"Approved redrafted": "اعتُمدت الصياغة المعدلة",
	"Assign directly": "إسناد مباشر",
	"Assigned": "مُسنَد",
	"Assigned body": "الجهة المسنَد إليها",
	"Assignment": "إسناد",
	"Assignment owner": "مسؤول الإسناد",
	"Begin the study": "بدء الدراسة",
	"Brief file": "ملف الموجز",
	"Brief — initial view": "الموجز — الرأي الأولي",
	"Brief — recommendation": "الموجز — التوصية",
	"Briefed": "أُعِدّ الموجز",
	"Channel": "قناة الإرسال",
	"Claimed urgency": "درجة الاستعجال المطلوبة",
	"Close": "إغلاق",
	"Closed": "مُغلق",
	"Comment on the study": "ملاحظات على الدراسة",
	"Committee": "لجنة",
	"Completion rate": "نسبة الإنجاز",
	"Conflict verdict": "نتيجة فحص التعارض",
	"Conflicts": "يتعارض",
	"Consistent": "متسق",
	"Consolidate the answers": "تجميع الردود",
	"Consolidated output": "المُخرَج الموحَّد",
	"Consolidating": "قيد تجميع الردود",
	"Coordinating body": "الجهة المنسِّقة",
	"Corrective action": "الإجراء التصحيحي",
	"Correspondence": "مراسلة",
	"Date received": "تاريخ الاستلام",
	"Deadline": "الموعد النهائي",
	"Decision Record": "سجل قرار",
	"Decision date": "تاريخ القرار",
	"Decision number": "رقم القرار",
	"Decision reference": "مرجع القرار",
	"Decision text": "نص القرار",
	"Deeper study": "دراسة معمّقة",
	"Delegation note": "ملاحظة التفويض",
	"Direct instruction": "توجيه مباشر",
	"Direct manager": "المدير المباشر",
	"Directed": "صدر التوجيه",
	"Direction pattern": "نمط التوجيه",
	"Direction round": "جولة التوجيه",
	"Direction wording": "صيغة التوجيه",
	"Directive": "توجيه",
	"Directive origin": "مصدر التوجيه",
	"Directive source": "مصدر التوجيه",
	"Directive text": "نص التوجيه",
	"Dismissed": "مرفوض",
	"Draft Decision Study": "دراسة مشروع قرار",
	"Draft text": "نص المشروع",
	"Drafted": "مسودة",
	"Drafting": "قيد الإعداد",
	"Duplicates": "مكرّر",
	"Duration (weeks)": "المدة (بالأسابيع)",
	"Effective date": "تاريخ النفاذ",
	"Escalation level": "مستوى التصعيد",
	"Evidence": "الدليل",
	"Evidence description": "وصف الإثبات",
	"Evidence documents": "مستندات الإثبات",
	"Evidence submitted": "ترحيل المستندات",
	"Execution Plan": "خطة تنفيذ",
	"Execution Plan Stage": "مرحلة خطة التنفيذ",
	"Executive Office": "المكتب التنفيذي",
	"Expected deliverable": "المُخرَج المتوقع",
	"Feasibility notes": "ملاحظات الجدوى",
	"Feasibility verdict": "نتيجة دراسة الجدوى",
	"Feasible": "مُجدٍ",
	"Feasible with conditions": "مُجدٍ بشروط",
	"File for information": "يُحفظ للعلم",
	"Finish in the Office": "يُنجَز في المكتب",
	"From stage": "من المرحلة",
	"Grade above": "الدرجة الأعلى",
	"Grounds": "المسوّغات",
	"H.E. decision": "قرار معاليه",
	"H.E. reason": "مسوّغ قرار معاليه",
	"Hand delivery": "تسليم باليد",
	"Immediate": "فوري",
	"In execution": "قيد التنفيذ",
	"In force": "نافذ",
	"In progress": "قيد التنفيذ",
	"Incoming Correspondence": "الطلب الوارد",
	"Information": "معلومات",
	"Instruction": "التعليمات",
	"Issue the decision": "إصدار القرار",
	"Issued": "صادر",
	"Issued by": "صادر عن",
	"Issuing authority": "جهة الإصدار",
	"Letter date": "تاريخ الخطاب",
	"Media commitment": "التزام إعلامي",
	"Named owner": "المسؤول المسمّى",
	"No action": "لا يلزم إجراء",
	"No conflict": "لا يوجد تعارض",
	"None": "لا يوجد",
	"Normal": "عادي",
	"Not feasible": "غير مُجدٍ",
	"Objection": "اعتراض",
	"Objection reason": "سبب الاعتراض",
	"Office recommendation": "توصية المكتب",
	"Official email": "البريد الإلكتروني الرسمي",
	"Official visit": "زيارة رسمية",
	"Open": "مفتوح",
	"Origin date": "تاريخ المصدر",
	"Origin record": "السجل المصدر",
	"Original attachment": "المرفق الأصلي",
	"Originating subsystem": "النظام الفرعي المصدر",
	"Outcome": "نتيجة البت",
	"Outcome reason": "مسوّغ النتيجة",
	"Outgoing Reply": "رد صادر",
	"Outgoing reference": "رقم الصادر",
	"Owning body for this stage": "الجهة المسؤولة عن المرحلة",
	"Parent referral": "الإحالة الأصل",
	"Plan approved": "اعتُمدت الخطة",
	"Plan end": "نهاية الخطة",
	"Plan required": "يتطلب خطة تنفيذ؟",
	"Plan start": "بداية الخطة",
	"Plan summary": "ملخص الخطة",
	"Planning": "قيد التخطيط",
	"Point referenced": "البند محل الاعتراض",
	"Position": "مرئيات",
	"Proposed redraft": "الصياغة المعدلة المقترحة",
	"Proposer": "مقدم المقترح",
	"Proposing body": "الجهة المقترِحة",
	"Raise to H.E.": "الرفع لمعاليه",
	"Raised": "مُقدَّم",
	"Raised by": "مقدم الاعتراض",
	"Received": "مُستلَم",
	"Received at": "تاريخ ووقت الاستلام",
	"Record the direction": "تسجيل التوجيه",
	"Record the response": "تسجيل الرد",
	"Recorded": "مُسجَّل",
	"Refer and consolidate": "إحالة وتجميع الردود",
	"Refer out": "إحالة لجهة مختصة",
	"Referral": "إحالة",
	"Referred to": "الجهة المحال إليها",
	"Reject": "الرفض",
	"Rejected": "مرفوض",
	"Reply attachments": "مرفقات الرد",
	"Reply text": "نص الرد",
	"Require an execution plan": "طلب خطة تنفيذ",
	"Required outcome": "النتيجة المطلوبة",
	"Resolve the objection": "البت في الاعتراض",
	"Resolved": "تم البت",
	"Response attachments": "مرفقات الرد",
	"Responsible officer": "المسؤول المباشر",
	"Return for clarification": "الإعادة للاستيضاح",
	"Returned for further study": "أُعيد لمزيد من الدراسة",
	"Revoked": "مُلغى",
	"Scope of work": "نطاق العمل",
	"Send the reply": "إرسال الرد",
	"Sending body": "الجهة المرسِلة",
	"Sent": "مُرسَل",
	"Sent on": "تاريخ الإرسال",
	"Source study": "الدراسة المصدر",
	"Stage": "رقم المرحلة",
	"Stage output": "مُخرَج المرحلة",
	"Stamp and submit": "ختم وتقديم",
	"Standing": "حالة النفاذ",
	"Start date": "تاريخ البدء",
	"Start the task": "بدء المهمة",
	"Start work": "بدء العمل",
	"Study deadline": "الموعد النهائي للدراسة",
	"Study report": "تقرير الدراسة",
	"Subject keywords": "الكلمات المفتاحية",
	"Submit evidence": "تقديم المستندات",
	"Submit for verification": "إرسال للتحقق",
	"Submitted": "مُقدَّم",
	"Superseded": "مُستبدَل",
	"Supersedes": "يحل محل",
	"Supporting document": "المستند المؤيِّد",
	"Task": "مهمة",
	"Task description": "وصف المهمة",
	"Their deadline": "الموعد المحدد من الجهة المرسِلة",
	"Their reference": "رقم خطاب الجهة",
	"Total duration (weeks)": "المدة الإجمالية (بالأسابيع)",
	"Under analysis": "قيد التحليل",
	"Under study": "قيد الدراسة",
	"Urgent": "عاجل",
	"Verification": "قيد التحقق",
	"Verification date": "تاريخ التحقق",
	"Verification notes": "ملاحظات التحقق",
	"Verified by": "تحقق منه",
	"Verified by and on": "تحقق منه وتاريخه",
	"Verify and close": "التحقق والإغلاق",
	"Version": "رقم الإصدار",
	"View submitted": "الرأي المقدَّم",
	"Visible to": "مرئي لـ",
	"Weekly meeting": "الاجتماع الأسبوعي",
	"What is actually wanted": "المطلوب فعلياً",
	"What is wanted": "المطلوب",
	"With H.E.": "لدى معاليه",
	"Write the brief": "إعداد الموجز",
	"تاريخ التنفيذ / تاريخ الاستحقاق": "تاريخ الاستحقاق",
}

// eoApplyArabicLabelsPending returns true if anything in the catalogue still
// has English in its Arabic slot that the map knows how to translate. A
// convergent step would run regardless, but the pending guard keeps the log
// honest ("step did nothing" vs "step was not needed").
func eoApplyArabicLabelsPending(db *gorm.DB) (bool, error) {
	// A single query would be ideal, but the five targets have different
	// shapes (one has a nested JSON). Counting rows one table at a time is
	// cheap at this scale (~hundreds of rows) and legible.
	var n int64

	if err := db.Raw(`SELECT COUNT(*) FROM eo_record_types WHERE name = name_ar`).Scan(&n).Error; err != nil {
		return false, fmt.Errorf("count untranslated types: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	if err := db.Raw(`SELECT COUNT(*) FROM eo_field_defs WHERE label = label_ar`).Scan(&n).Error; err != nil {
		return false, fmt.Errorf("count untranslated fields: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	if err := db.Raw(`SELECT COUNT(*) FROM workflow_states ws
		JOIN workflows w ON w.id = ws.workflow_id
		WHERE w.code LIKE 'eo_%' AND ws.name = ws.name_ar`).Scan(&n).Error; err != nil {
		return false, fmt.Errorf("count untranslated states: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	if err := db.Raw(`SELECT COUNT(*) FROM workflow_transitions wt
		JOIN workflows w ON w.id = wt.workflow_id
		WHERE w.code LIKE 'eo_%' AND wt.name = wt.name_ar`).Scan(&n).Error; err != nil {
		return false, fmt.Errorf("count untranslated transitions: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	// Dropdown options: if any config holds an option whose label_ar equals
	// its label AND the label is in our map, we have work to do.
	return eoDropdownOptionsHaveUntranslated(db)
}

func eoDropdownOptionsHaveUntranslated(db *gorm.DB) (bool, error) {
	type row struct{ ID, Config string }
	var rows []row
	if err := db.Raw(`SELECT id, config::text AS config FROM eo_field_defs WHERE kind = 'dropdown'`).Scan(&rows).Error; err != nil {
		return false, fmt.Errorf("list dropdown fields: %w", err)
	}
	for _, r := range rows {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(r.Config), &cfg); err != nil {
			continue
		}
		opts, _ := cfg["options"].([]any)
		for _, o := range opts {
			opt, _ := o.(map[string]any)
			label, _ := opt["label"].(string)
			labelAr, _ := opt["label_ar"].(string)
			if label == "" || labelAr != label {
				continue
			}
			if _, ok := eoArabicLabels[label]; ok {
				return true, nil
			}
		}
	}
	return false, nil
}

// eoApplyArabicLabels is the convergent step. It updates label_ar wherever the
// current value still equals the English and the map knows an Arabic for it.
// An administrator's prior edit is left alone: it is detected as "label_ar !=
// label" and skipped.
func eoApplyArabicLabels(tx *gorm.DB) (bool, error) {
	changed := false

	for en, ar := range eoArabicLabels {
		// Record types
		res := tx.Exec(`UPDATE eo_record_types SET name_ar = ? WHERE name = ? AND name_ar = name`, ar, en)
		if res.Error != nil {
			return false, fmt.Errorf("update record_types for %q: %w", en, res.Error)
		}
		if res.RowsAffected > 0 {
			changed = true
		}

		// Field definitions
		res = tx.Exec(`UPDATE eo_field_defs SET label_ar = ?, ar_untranslated = false WHERE label = ? AND label_ar = label`, ar, en)
		if res.Error != nil {
			return false, fmt.Errorf("update field_defs for %q: %w", en, res.Error)
		}
		if res.RowsAffected > 0 {
			changed = true
		}

		// Workflow states (only EO workflows)
		res = tx.Exec(`UPDATE workflow_states SET name_ar = ? WHERE id IN (
			SELECT ws.id FROM workflow_states ws
			JOIN workflows w ON w.id = ws.workflow_id
			WHERE w.code LIKE 'eo_%' AND ws.name = ? AND ws.name_ar = ws.name
		)`, ar, en)
		if res.Error != nil {
			return false, fmt.Errorf("update workflow_states for %q: %w", en, res.Error)
		}
		if res.RowsAffected > 0 {
			changed = true
		}

		// Workflow transitions (only EO workflows)
		res = tx.Exec(`UPDATE workflow_transitions SET name_ar = ? WHERE id IN (
			SELECT wt.id FROM workflow_transitions wt
			JOIN workflows w ON w.id = wt.workflow_id
			WHERE w.code LIKE 'eo_%' AND wt.name = ? AND wt.name_ar = wt.name
		)`, ar, en)
		if res.Error != nil {
			return false, fmt.Errorf("update workflow_transitions for %q: %w", en, res.Error)
		}
		if res.RowsAffected > 0 {
			changed = true
		}
	}

	// Dropdown option labels live inside eo_field_defs.config JSON.
	type row struct{ ID, Config string }
	var rows []row
	if err := tx.Raw(`SELECT id, config::text AS config FROM eo_field_defs WHERE kind = 'dropdown'`).Scan(&rows).Error; err != nil {
		return false, fmt.Errorf("list dropdown fields: %w", err)
	}
	for _, r := range rows {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(r.Config), &cfg); err != nil {
			continue
		}
		opts, _ := cfg["options"].([]any)
		dirty := false
		for _, o := range opts {
			opt, _ := o.(map[string]any)
			label, _ := opt["label"].(string)
			labelAr, _ := opt["label_ar"].(string)
			if label == "" || labelAr != label {
				continue
			}
			if ar, ok := eoArabicLabels[label]; ok {
				opt["label_ar"] = ar
				dirty = true
			}
		}
		if !dirty {
			continue
		}
		newCfg, err := json.Marshal(cfg)
		if err != nil {
			return false, fmt.Errorf("re-serialise dropdown %s: %w", r.ID, err)
		}
		if err := tx.Exec(`UPDATE eo_field_defs SET config = ? WHERE id = ?`, string(newCfg), r.ID).Error; err != nil {
			return false, fmt.Errorf("update dropdown %s: %w", r.ID, err)
		}
		changed = true
	}

	return changed, nil
}
