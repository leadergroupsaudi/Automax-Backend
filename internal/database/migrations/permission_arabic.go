package migrations

import (
	"gorm.io/gorm"
)

// permissionModuleArabic maps a permission's module code to its Arabic label.
// Kept separate from the per-permission table because many permissions share a
// module, so the module wording only needs to be decided once.
var permissionModuleArabic = map[string]string{
	"action-logs":            "سجلات الإجراءات",
	"application-links":      "روابط التطبيقات",
	"benchmark":              "المعايير المرجعية",
	"call-logs":              "سجلات المكالمات",
	"caller-sentiment":       "مشاعر المتصلين",
	"categories":             "الفئات",
	"classifications":        "التصنيفات",
	"communication-tracking": "تتبع المراسلات",
	"complaints":             "الشكاوى",
	"corrective_action":      "الإجراءات التصحيحية",
	"dashboard":              "لوحة المعلومات",
	"dispatch":               "التوزيع",
	"departments":            "الإدارات",
	"escalation-groups":      "مجموعات التصعيد",
	"escalation-policies":    "سياسات التصعيد",
	"extensions":             "التحويلات الهاتفية",
	"goals":                  "الأهداف",
	"incidents":              "الحوادث",
	"kpi":                    "مؤشرات الأداء",
	"license":                "الترخيص",
	"locations":              "المواقع",
	"lookups":                "القوائم المرجعية",
	"notifications":          "الإشعارات",
	"perf":                   "أداء المؤشرات",
	"permissions":            "الصلاحيات",
	"queries":                "الاستفسارات",
	"reports":                "التقارير",
	"requests":               "الطلبات",
	"roles":                  "الأدوار",
	"segment":                "بيانات التقسيم",
	"settings":               "الإعدادات",
	"targets":                "المستهدفات",
	"templates":              "القوالب",
	"users":                  "المستخدمون",
	"workflows":              "مسارات العمل",
}

// permissionNameArabic maps a permission code to its Arabic name and description.
// Keyed by code because that is the stable, unique identifier — English names may
// be edited by admins, codes may not.
var permissionNameArabic = map[string]struct{ Name, Description string }{
	// Action logs
	"action-logs:view":   {"عرض سجلات الإجراءات", "عرض سجلات الإجراءات"},
	"action-logs:delete": {"حذف سجلات الإجراءات", "حذف سجلات الإجراءات وتنظيفها"},

	// Application links
	"application-links:view":      {"عرض روابط التطبيقات", "عرض روابط التطبيقات"},
	"application-links:create":    {"إنشاء روابط التطبيقات", "إنشاء روابط التطبيقات"},
	"application-links:update":    {"تحديث روابط التطبيقات", "تحديث روابط التطبيقات"},
	"application-links:delete":    {"حذف روابط التطبيقات", "حذف روابط التطبيقات"},
	"application-links:dashboard": {"الوصول إلى روابط التطبيقات في لوحة المعلومات", "عرض روابط التطبيقات وتشغيلها من لوحة المعلومات"},

	// Benchmarks & segmentation
	"benchmark:manage": {"إدارة المعايير المرجعية", "إنشاء وتحديث المعايير المرجعية لمؤشرات الأداء"},
	"segment:manage":   {"إدارة بيانات التقسيم", "إنشاء وتحديث بيانات تقسيم مؤشرات الأداء"},

	// Caller sentiment
	"caller-sentiment:view":   {"عرض مشاعر المتصلين", "عرض جميع سجلات مشاعر المتصلين والملخصات"},
	"caller-sentiment:create": {"تسجيل مشاعر المتصل", "تسجيل مدخل مشاعر بعد المكالمة"},

	// Call logs
	"call-logs:view":     {"عرض سجلات المكالمات", "عرض سجلات المكالمات"},
	"call-logs:view_all": {"عرض جميع سجلات المكالمات", "عرض سجلات مكالمات الموظفين الآخرين بغض النظر عن المشاركة"},
	"call-logs:create":   {"إنشاء سجلات المكالمات", "إنشاء سجلات المكالمات"},
	"call-logs:update":   {"تحديث سجلات المكالمات", "تحديث سجلات المكالمات"},
	"call-logs:delete":   {"حذف سجلات المكالمات", "حذف سجلات المكالمات"},

	// Categories
	"categories:view":   {"عرض الفئات", "عرض الفئات"},
	"categories:create": {"إنشاء الفئات", "إنشاء الفئات"},
	"categories:update": {"تحديث الفئات", "تحديث الفئات"},
	"categories:delete": {"حذف الفئات", "حذف الفئات"},

	// Classifications
	"classifications:view":   {"عرض التصنيفات", "عرض التصنيفات"},
	"classifications:create": {"إنشاء التصنيفات", "إنشاء التصنيفات"},
	"classifications:update": {"تحديث التصنيفات", "تحديث التصنيفات"},
	"classifications:delete": {"حذف التصنيفات", "حذف التصنيفات"},

	// Communication tracking
	"communication-tracking:view":   {"عرض لوحة تتبع المراسلات", "عرض لوحة تتبع المراسلات عبر القنوات (حالة تسليم الرسائل النصية والبريد الإلكتروني وواتساب)"},
	"communication-tracking:update": {"تحديث تتبع المراسلات", "إعادة إرسال إشعار نصي أو بريدي أو واتساب فاشل أو غير قابل للتسليم أو منتهي الصلاحية يدويًا"},

	// Complaints
	"complaints:view":       {"عرض الشكاوى", "عرض الشكاوى"},
	"complaints:view_all":   {"عرض جميع الشكاوى", "عرض جميع الشكاوى بغض النظر عن الإسناد"},
	"complaints:create":     {"إنشاء الشكاوى", "إنشاء شكاوى جديدة"},
	"complaints:update":     {"تحديث الشكاوى", "تحديث حقول الشكوى"},
	"complaints:delete":     {"حذف الشكاوى", "حذف الشكاوى"},
	"complaints:assign":     {"إسناد الشكاوى", "إسناد الشكاوى وإعادة إسنادها"},
	"complaints:comment":    {"التعليق على الشكاوى", "إضافة تعليقات على الشكاوى"},
	"complaints:transition": {"تغيير حالة الشكاوى", "تنفيذ انتقالات حالة الشكوى"},

	// Corrective actions
	"corrective_action:manage": {"إدارة الإجراءات التصحيحية", "إنشاء وإغلاق الإجراءات التصحيحية لمؤشرات الأداء"},

	// Dashboard
	"dashboard:admin":      {"لوحة معلومات الإدارة", "الوصول إلى بطاقات قسم الإدارة في لوحة المعلومات"},
	"dashboard:ccm":        {"لوحة معلومات إدارة مركز الاتصال", "الوصول إلى بطاقات مركز الاتصال في لوحة المعلومات"},
	"dashboard:complaints": {"لوحة معلومات الشكاوى", "الوصول إلى بطاقات الشكاوى في لوحة المعلومات"},
	"dashboard:goals":      {"لوحة معلومات الأهداف", "الوصول إلى بطاقات الأهداف في لوحة المعلومات"},
	"dashboard:incidents":  {"لوحة معلومات الحوادث", "الوصول إلى بطاقات الحوادث في لوحة المعلومات"},
	"dashboard:queries":    {"لوحة معلومات الاستفسارات", "الوصول إلى بطاقات الاستفسارات في لوحة المعلومات"},
	"dashboard:requests":   {"لوحة معلومات الطلبات", "الوصول إلى بطاقات الطلبات في لوحة المعلومات"},
	"dashboard:workflows":  {"لوحة معلومات مسارات العمل", "الوصول إلى بطاقات مسارات العمل في لوحة المعلومات"},
	"dashboard:dispatch":   {"لوحة معلومات التوزيع", "الوصول إلى بطاقة إدارة التوزيع في لوحة المعلومات"},

	// Dispatch
	"dispatch:view":   {"عرض التوزيع", "عرض الوحدات وأنواع الوحدات ومحطات الإطفاء"},
	"dispatch:create": {"إنشاء التوزيع", "إنشاء الوحدات وأنواع الوحدات ومحطات الإطفاء"},
	"dispatch:update": {"تحديث التوزيع", "تحديث الوحدات وأنواع الوحدات ومحطات الإطفاء"},
	"dispatch:delete": {"حذف التوزيع", "حذف الوحدات وأنواع الوحدات ومحطات الإطفاء"},

	// Departments
	"departments:view":   {"عرض الإدارات", "عرض الإدارات"},
	"departments:create": {"إنشاء الإدارات", "إنشاء الإدارات"},
	"departments:update": {"تحديث الإدارات", "تحديث الإدارات"},
	"departments:delete": {"حذف الإدارات", "حذف الإدارات"},

	// Escalation
	"escalation-groups:view":         {"عرض مجموعات التصعيد", "عرض قائمة مجموعات التصعيد وتفاصيلها"},
	"escalation-groups:create":       {"إنشاء مجموعة تصعيد", "إنشاء مجموعات تصعيد جديدة"},
	"escalation-groups:update":       {"تحديث مجموعة التصعيد", "تحديث إعدادات مجموعة التصعيد"},
	"escalation-groups:delete":       {"حذف مجموعة التصعيد", "حذف مجموعات التصعيد"},
	"escalation-groups:assign_users": {"إسناد المستخدمين إلى مجموعة التصعيد", "إضافة المستخدمين إلى مجموعات التصعيد أو إزالتهم منها"},
	"escalation-groups:manage_rules": {"إدارة قواعد التصعيد", "إعداد قواعد تكرار التصعيد والقناة والتصنيف"},
	"escalation-policies:create":     {"إنشاء سياسة تصعيد", "إنشاء سياسات تصعيد جديدة"},

	// Extensions
	"extensions:view":    {"عرض التحويلات الهاتفية", "عرض التحويلات الهاتفية وحالة الإسناد"},
	"extensions:create":  {"إنشاء التحويلات الهاتفية", "إنشاء تحويلات هاتفية جديدة على المقسم"},
	"extensions:assign":  {"إسناد التحويلات الهاتفية", "إسناد التحويلات الهاتفية للمستخدمين وإعادة إسنادها"},
	"extensions:release": {"إلغاء إسناد التحويلات الهاتفية", "إلغاء إسناد التحويلات الهاتفية من المستخدمين"},

	// Goals
	"goals:view":    {"عرض الأهداف", "عرض الأهداف"},
	"goals:create":  {"إنشاء الأهداف", "إنشاء أهداف جديدة"},
	"goals:update":  {"تحديث الأهداف", "تحديث الأهداف"},
	"goals:delete":  {"حذف الأهداف", "حذف الأهداف"},
	"goals:assign":  {"إسناد الأهداف", "إسناد المتعاونين على الأهداف"},
	"goals:approve": {"اعتماد الأهداف", "اعتماد أو رفض أدلة الأهداف"},
	"goals:manage":  {"إدارة هيكل الأهداف", "إنشاء وتحديث وحذف البيانات الرئيسية لهيكل الأهداف"},

	// Incidents
	"incidents:view":                      {"عرض الحوادث", "عرض الحوادث"},
	"incidents:view_all":                  {"عرض جميع الحوادث", "عرض جميع الحوادث بغض النظر عن الإسناد"},
	"incidents:view_department_only":      {"عرض حوادث الإدارة فقط", "تقييد عرض الحوادث على إدارة المستخدم"},
	"incidents:create":                    {"إنشاء الحوادث", "إنشاء حوادث جديدة"},
	"incidents:update":                    {"تحديث الحوادث", "تحديث حقول الحادث"},
	"incidents:delete":                    {"حذف الحوادث", "حذف الحوادث"},
	"incidents:assign":                    {"إسناد الحوادث", "إسناد الحوادث وإعادة إسنادها"},
	"incidents:comment":                   {"التعليق على الحوادث", "إضافة تعليقات على الحوادث"},
	"incidents:transition":                {"تغيير حالة الحوادث", "تنفيذ انتقالات الحالة"},
	"incidents:merge":                     {"دمج الحوادث", "دمج عدة حوادث في حادث واحد"},
	"incidents:share":                     {"مشاركة الحوادث", "مشاركة تفاصيل الحادث مع أطراف خارجية"},
	"incidents:edit-closed":               {"تعديل الحوادث المغلقة", "تعديل ملخص ووصف الحوادث المغلقة"},
	"incidents:request-info":              {"طلب معلومات عن الحوادث", "طلب معلومات إضافية من المواطنين"},
	"incidents:verify-nasaq":              {"التحقق من نساق", "تشغيل التحقق اليدوي من تصريح حفر نساق لبلاغ"},
	"incidents:manage_sla":                {"إدارة اتفاقية مستوى الخدمة", "تجاوز إعدادات اتفاقية مستوى الخدمة"},
	"incidents:filter_reporter_phone":     {"تصفية الحوادث برقم هاتف المُبلِّغ", "تصفية الحوادث حسب رقم هاتف المُبلِّغ"},
	"incidents:upload-attachment-gallery": {"رفع معرض المرفقات", "رفع مرفقات إلى معرض مرفقات الحادث"},

	// KPI dictionary
	"kpi:view":    {"عرض قاموس مؤشرات الأداء", "عرض تعريفات مؤشرات الأداء"},
	"kpi:create":  {"إنشاء تعريفات مؤشرات الأداء", "إنشاء تعريفات جديدة لمؤشرات الأداء"},
	"kpi:update":  {"تحديث تعريفات مؤشرات الأداء", "تعديل بيانات مؤشر الأداء ومعادلته ومستهدفاته"},
	"kpi:delete":  {"حذف تعريفات مؤشرات الأداء", "حذف سجلات مؤشرات الأداء حذفًا مبدئيًا"},
	"kpi:assign":  {"إسناد المتعاونين على مؤشرات الأداء", "إضافة أو إزالة المتعاونين على تعريف مؤشر الأداء"},
	"kpi:approve": {"اعتماد أداء مؤشرات الأداء", "اعتماد أو رفض مدخلات أداء المؤشرات من صندوق الاعتمادات"},

	// License
	"license:view":   {"عرض الترخيص", "عرض حالة الترخيص ومعلوماته"},
	"license:manage": {"إدارة الترخيص", "تنشيط مفاتيح الترخيص وتعطيلها وإدارتها"},

	// Locations
	"locations:view":   {"عرض المواقع", "عرض المواقع"},
	"locations:create": {"إنشاء المواقع", "إنشاء المواقع"},
	"locations:update": {"تحديث المواقع", "تحديث المواقع"},
	"locations:delete": {"حذف المواقع", "حذف المواقع"},

	// Lookups
	"lookups:view":   {"عرض القوائم المرجعية", "عرض فئات القوائم المرجعية وقيمها"},
	"lookups:create": {"إنشاء القوائم المرجعية", "إنشاء فئات القوائم المرجعية وقيمها"},
	"lookups:update": {"تحديث القوائم المرجعية", "تحديث فئات القوائم المرجعية وقيمها"},
	"lookups:delete": {"حذف القوائم المرجعية", "حذف فئات القوائم المرجعية وقيمها"},

	// Notifications
	"notifications:read":   {"عرض الإشعارات", "عرض سجلات الإشعارات"},
	"notifications:create": {"إنشاء مسودات الإشعارات", "إنشاء مسودات الإشعارات"},
	"notifications:update": {"تحديث مسودات الإشعارات", "تحديث مسودات الإشعارات"},
	"notifications:delete": {"حذف الإشعارات", "حذف سجلات الإشعارات"},
	"notifications:send":   {"إرسال الإشعارات", "إرسال إشعارات البريد الإلكتروني والرسائل النصية"},

	// Performance
	"perf:view":            {"عرض بيانات الأداء", "عرض بيانات أداء مؤشرات الأداء"},
	"perf:submit":          {"رفع الأداء", "رفع النتائج الفعلية الربعية للمراجعة"},
	"perf:review":          {"مراجعة الأداء", "بدء عملية مراجعة الأداء"},
	"perf:approve":         {"اعتماد الأداء", "اعتماد مدخلات الأداء التي تمت مراجعتها"},
	"perf:reject":          {"رفض الأداء", "الرفض والإرجاع للتعديل"},
	"perf:request_changes": {"طلب تعديلات على الأداء", "إرجاع مدخل أداء مرفوع لإجراء تعديلات"},
	"perf:publish":         {"نشر الأداء", "نشر الأداء المعتمد على لوحات المعلومات"},
	"perf:override_lock":   {"تجاوز قفل الاعتماد", "تعديل أو حذف مدخل أداء تم اعتماده مسبقًا"},

	// Permissions
	"permissions:view":   {"عرض الصلاحيات", "عرض الصلاحيات"},
	"permissions:manage": {"إدارة الصلاحيات", "إدارة الصلاحيات"},

	// Queries
	"queries:view":       {"عرض الاستفسارات", "عرض الاستفسارات"},
	"queries:view_all":   {"عرض جميع الاستفسارات", "عرض جميع الاستفسارات بغض النظر عن الإسناد"},
	"queries:create":     {"إنشاء الاستفسارات", "إنشاء استفسارات جديدة"},
	"queries:update":     {"تحديث الاستفسارات", "تحديث حقول الاستفسار"},
	"queries:delete":     {"حذف الاستفسارات", "حذف الاستفسارات"},
	"queries:assign":     {"إسناد الاستفسارات", "إسناد الاستفسارات وإعادة إسنادها"},
	"queries:comment":    {"التعليق على الاستفسارات", "إضافة تعليقات على الاستفسارات"},
	"queries:transition": {"تغيير حالة الاستفسارات", "تنفيذ انتقالات حالة الاستفسار"},

	// Reports
	"reports:view":   {"عرض التقارير", "عرض التقارير"},
	"reports:create": {"إنشاء التقارير", "إنشاء تقارير جديدة"},
	"reports:update": {"تحديث التقارير", "تحديث التقارير"},
	"reports:delete": {"حذف التقارير", "حذف التقارير"},

	// Requests
	"requests:view":       {"عرض الطلبات", "عرض الطلبات"},
	"requests:view_all":   {"عرض جميع الطلبات", "عرض جميع الطلبات بغض النظر عن الإسناد"},
	"requests:create":     {"إنشاء الطلبات", "إنشاء طلبات جديدة"},
	"requests:update":     {"تحديث الطلبات", "تحديث حقول الطلب"},
	"requests:delete":     {"حذف الطلبات", "حذف الطلبات"},
	"requests:assign":     {"إسناد الطلبات", "إسناد الطلبات وإعادة إسنادها"},
	"requests:comment":    {"التعليق على الطلبات", "إضافة تعليقات على الطلبات"},
	"requests:transition": {"تغيير حالة الطلبات", "تنفيذ انتقالات حالة الطلب"},

	// Roles
	"roles:view":   {"عرض الأدوار", "عرض قائمة الأدوار"},
	"roles:create": {"إنشاء الأدوار", "إنشاء أدوار جديدة"},
	"roles:update": {"تحديث الأدوار", "تحديث الأدوار"},
	"roles:delete": {"حذف الأدوار", "حذف الأدوار"},

	// Settings
	"settings:view":   {"عرض الإعدادات", "عرض إعدادات النظام"},
	"settings:update": {"تحديث الإعدادات", "تحديث إعدادات النظام"},

	// Targets
	"targets:view":    {"عرض المستهدفات", "عرض المستهدفات السنوية لمؤشرات الأداء"},
	"targets:set":     {"تحديد المستهدفات", "إنشاء وتحديث المستهدفات السنوية"},
	"targets:approve": {"اعتماد المستهدفات", "اعتماد المستهدفات المرفوعة"},
	"targets:reject":  {"رفض المستهدفات", "رفض المستهدفات المرفوعة"},
	"targets:delete":  {"حذف المستهدفات", "حذف المستهدفات السنوية المسودة أو المرجعة أو المرفوضة"},

	// Templates
	"templates:read":   {"عرض القوالب", "عرض قوالب الإشعارات"},
	"templates:create": {"إنشاء القوالب", "إنشاء قوالب الإشعارات"},
	"templates:update": {"تحديث القوالب", "تحديث قوالب الإشعارات"},
	"templates:delete": {"حذف القوالب", "حذف قوالب الإشعارات"},

	// Users
	"users:view":                 {"عرض المستخدمين", "عرض قائمة المستخدمين وتفاصيلهم"},
	"users:view_department_only": {"عرض مستخدمي الإدارة فقط", "تقييد عرض المستخدمين على إدارة المستخدم"},
	"users:create":               {"إنشاء المستخدمين", "إنشاء مستخدمين جدد"},
	"users:update":               {"تحديث المستخدمين", "تحديث معلومات المستخدم"},
	"users:delete":               {"حذف المستخدمين", "حذف المستخدمين"},
	"users:reset_password":       {"إعادة تعيين كلمة مرور المستخدم", "إعادة تعيين كلمة مرور المستخدم"},

	// Workflows
	"workflows:view":   {"عرض مسارات العمل", "عرض قوالب مسارات العمل"},
	"workflows:create": {"إنشاء مسارات العمل", "إنشاء قوالب مسارات العمل"},
	"workflows:update": {"تحديث مسارات العمل", "تحديث قوالب مسارات العمل"},
	"workflows:delete": {"حذف مسارات العمل", "حذف قوالب مسارات العمل"},
	"workflows:design": {"تصميم مسارات العمل", "الوصول إلى مصمم مسارات العمل"},
}

// permissionActionArabic maps a permission's action code to its Arabic label.
// Keyed by the raw `action` column value stored on the row — note this is not always
// the code suffix after the colon (e.g. code "perf:override_lock" stores action
// "override", and code "incidents:edit-closed" stores action "edit_closed") — so
// these pairs are copied verbatim from the seed data in postgres.go.
var permissionActionArabic = map[string]string{
	"admin":                 "مسؤول",
	"approve":               "موافقة",
	"assign":                "تعيين",
	"assign_users":          "تعيين المستخدمين",
	"ccm":                   "إدارة علاقات المواطنين",
	"comment":               "تعليق",
	"complaints":            "الشكاوى",
	"create":                "إنشاء",
	"dashboard":             "لوحة التحكم",
	"delete":                "حذف",
	"design":                "تصميم",
	"edit_closed":           "تعديل المغلق",
	"filter_reporter_phone": "تصفية حسب هاتف المبلغ",
	"goals":                 "الأهداف",
	"incidents":             "البلاغات",
	"manage":                "إدارة",
	"manage_rules":          "إدارة القواعد",
	"manage_sla":            "إدارة اتفاقية مستوى الخدمة",
	"merge":                 "دمج",
	"override":              "تجاوز",
	"publish":               "نشر",
	"queries":               "الاستفسارات",
	"read":                  "قراءة",
	"reject":                "رفض",
	"release":               "تحرير",
	"request_changes":       "طلب تعديلات",
	"request_info":          "طلب معلومات",
	"requests":              "الطلبات",
	"reset_password":        "إعادة تعيين كلمة المرور",
	"review":                "مراجعة",
	"send":                  "إرسال",
	"set":                   "تحديد",
	"share":                 "مشاركة",
	"submit":                "تقديم",
	"transition":            "انتقال الحالة",
	"update":                "تحديث",
	"view":                  "عرض",
	"view_all":              "عرض الكل",
	"view_department_only":  "عرض القسم فقط",
	"workflows":             "سير العمل",
}

// MigratePermissionArabic backfills the Arabic columns (name_ar, description_ar,
// module_ar, action_ar) on the permissions table.
//
// The seed in postgres.go only inserts permissions that do not exist yet — it has
// no update branch — so every database created before these columns existed holds
// rows with empty Arabic values that seeding will never fill. This migration fills
// them once.
//
// Only blank values are written, so a translation an admin has edited through the
// API is never overwritten. The whole thing is skipped outright once no blanks
// remain, which is the case on every boot after the first.
func MigratePermissionArabic(db *gorm.DB) error {
	var pending int64
	if err := db.Table("permissions").
		Where("COALESCE(name_ar, '') = '' OR COALESCE(description_ar, '') = '' OR COALESCE(module_ar, '') = '' OR COALESCE(action_ar, '') = ''").
		Count(&pending).Error; err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Module labels are shared, so one statement per module covers every
		// permission under it — including any added outside the seed list.
		for module, moduleAr := range permissionModuleArabic {
			if err := tx.Exec(
				`UPDATE permissions SET module_ar = ? WHERE module = ? AND COALESCE(module_ar, '') = ''`,
				moduleAr, module,
			).Error; err != nil {
				return err
			}
		}

		// Action labels are shared across modules the same way, so one statement
		// per action covers every permission using it.
		for action, actionAr := range permissionActionArabic {
			if err := tx.Exec(
				`UPDATE permissions SET action_ar = ? WHERE action = ? AND COALESCE(action_ar, '') = ''`,
				actionAr, action,
			).Error; err != nil {
				return err
			}
		}

		for code, tr := range permissionNameArabic {
			if err := tx.Exec(`
				UPDATE permissions SET
					name_ar = CASE WHEN COALESCE(name_ar, '') = '' THEN ? ELSE name_ar END,
					description_ar = CASE WHEN COALESCE(description_ar, '') = '' THEN ? ELSE description_ar END
				WHERE code = ?`,
				tr.Name, tr.Description, code,
			).Error; err != nil {
				return err
			}
		}

		return nil
	})
}
