package services

import "fmt"

// Arabic text builders for incident in-app notifications.
// English text is built inline at each call site; these produce the Arabic counterpart
// stored in subject_ar / body_ar and returned when Accept-Language: ar.

func IncidentUpdatedTextsAr(incidentNumber, title string) (subject, body string) {
	return fmt.Sprintf("تم تحديث البلاغ رقم %s", incidentNumber),
		fmt.Sprintf("تم تحديث بلاغ \"%s\" (%s)", title, incidentNumber)
}

func IncidentAssignedTransitionTextsAr(incidentNumber, title, stateName string) (subject, body string) {
	return fmt.Sprintf("بلاغ رقم %s مسند اليك", incidentNumber),
		fmt.Sprintf("بلاغ \"%s\" اسند لك. تم تغيير الحالة الى : %s", title, stateName)
}

func IncidentAssignedDirectTextsAr(incidentNumber, title string) (subject, body string) {
	return fmt.Sprintf("بلاغ رقم %s مسند اليك", incidentNumber),
		fmt.Sprintf("بلاغ \"%s\" اسند لك", title)
}

func PartialCloseExpiryTextsAr(incidentNumber, title, revertStateName, timeStr, expiresAt string) (subject, body string) {
	subject = fmt.Sprintf("البلاغ %s: موعد انتهاء الإغلاق الجزئي قريب", incidentNumber)
	body = fmt.Sprintf(
		"سيتم إعادة هذا البلاغ تلقائياً إلى '%s' إذا لم يتم إغلاقه خلال %s.\n\nرقم البلاغ: %s\nالعنوان: %s\nتاريخ الانتهاء: %s",
		revertStateName, timeStr, incidentNumber, title, expiresAt,
	)
	return
}

func DispatchAssignedTextsAr(incidentNumber, title string) (subject, body string) {
	return fmt.Sprintf("إرسال: %s", incidentNumber),
		fmt.Sprintf("تم تكليفك بالتوجه إلى بلاغ \"%s\".", title)
}
