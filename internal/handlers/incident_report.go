package handlers

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
)

// GenerateReport generates a detailed report for an incident
func (h *IncidentHandler) GenerateReport(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	// Get incident detail response for display
	incident, err := h.service.GetIncident(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Incident not found")
	}

	// Get raw incident with file paths for attachments
	rawIncident, err := h.incidentRepo.FindByIDWithRelations(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Incident not found")
	}

	// Build report data
	report := map[string]interface{}{
		"generated_at": time.Now().Format(time.RFC3339),
		"incident":     incident,
		"comments":     incident.Comments,
		"attachments":  incident.Attachments,
		"history":      incident.TransitionHistory,
	}

	// Format parameter determines output format
	format := c.Query("format", "pdf")

	switch format {
	case "pdf":
		// Generate PDF report
		pdf := gofpdf.New("P", "mm", "A4", "")
		pdf.SetAutoPageBreak(true, 15)
		pdf.AddPage()

		// Title
		pdf.SetFont("Arial", "B", 20)
		pdf.Cell(0, 10, "INCIDENT REPORT")
		pdf.Ln(12)

		// Generated timestamp
		pdf.SetFont("Arial", "I", 10)
		pdf.SetTextColor(128, 128, 128)
		pdf.Cell(0, 6, "Generated: "+time.Now().Format("2006-01-02 15:04:05"))
		pdf.Ln(10)
		pdf.SetTextColor(0, 0, 0)

		// Incident Details Section
		pdf.SetFont("Arial", "B", 14)
		pdf.SetFillColor(240, 240, 240)
		pdf.CellFormat(0, 8, "Incident Details", "", 1, "L", true, 0, "")
		pdf.Ln(2)

		pdf.SetFont("Arial", "", 10)
		pdf.Cell(45, 6, "Incident Number:")
		pdf.SetFont("Arial", "B", 10)
		pdf.Cell(0, 6, incident.IncidentNumber)
		pdf.Ln(6)

		pdf.SetFont("Arial", "", 10)
		pdf.Cell(45, 6, "Title:")
		pdf.SetFont("Arial", "B", 10)
		pdf.MultiCell(0, 6, incident.Title, "", "L", false)

		pdf.SetFont("Arial", "", 10)
		pdf.Cell(45, 6, "Status:")
		pdf.SetFont("Arial", "B", 10)
		if incident.CurrentState != nil {
			pdf.Cell(0, 6, incident.CurrentState.Name)
		}
		pdf.Ln(6)

		pdf.SetFont("Arial", "", 10)
		pdf.Cell(45, 6, "Record Type:")
		pdf.SetFont("Arial", "B", 10)
		pdf.Cell(0, 6, incident.RecordType)
		pdf.Ln(6)

		if incident.Classification != nil {
			pdf.SetFont("Arial", "", 10)
			pdf.Cell(45, 6, "Classification:")
			pdf.SetFont("Arial", "B", 10)
			pdf.Cell(0, 6, incident.Classification.Name)
			pdf.Ln(6)
		}

		if incident.Department != nil {
			pdf.SetFont("Arial", "", 10)
			pdf.Cell(45, 6, "Department:")
			pdf.SetFont("Arial", "B", 10)
			pdf.Cell(0, 6, incident.Department.Name)
			pdf.Ln(6)
		}

		if incident.Assignee != nil {
			pdf.SetFont("Arial", "", 10)
			pdf.Cell(45, 6, "Assigned To:")
			pdf.SetFont("Arial", "B", 10)
			pdf.Cell(0, 6, fmt.Sprintf("%s %s", incident.Assignee.FirstName, incident.Assignee.LastName))
			pdf.Ln(6)
		}

		if incident.Reporter != nil {
			pdf.SetFont("Arial", "", 10)
			pdf.Cell(45, 6, "Reported By:")
			pdf.SetFont("Arial", "B", 10)
			pdf.Cell(0, 6, fmt.Sprintf("%s %s", incident.Reporter.FirstName, incident.Reporter.LastName))
			pdf.Ln(6)
		}

		pdf.SetFont("Arial", "", 10)
		pdf.Cell(45, 6, "Created:")
		pdf.SetFont("Arial", "B", 10)
		pdf.Cell(0, 6, incident.CreatedAt.Format("2006-01-02 15:04:05"))
		pdf.Ln(6)

		pdf.SetFont("Arial", "", 10)
		pdf.Cell(45, 6, "Updated:")
		pdf.SetFont("Arial", "B", 10)
		pdf.Cell(0, 6, incident.UpdatedAt.Format("2006-01-02 15:04:05"))
		pdf.Ln(10)

		// Description
		if incident.Description != "" {
			pdf.SetFont("Arial", "B", 12)
			pdf.CellFormat(0, 8, "Description", "", 1, "L", true, 0, "")
			pdf.Ln(2)
			pdf.SetFont("Arial", "", 10)
			pdf.MultiCell(0, 6, incident.Description, "", "L", false)
			pdf.Ln(5)
		}

		// Comments Section
		if len(incident.Comments) > 0 {
			pdf.SetFont("Arial", "B", 14)
			pdf.SetFillColor(240, 240, 240)
			pdf.CellFormat(0, 8, fmt.Sprintf("Comments (%d)", len(incident.Comments)), "", 1, "L", true, 0, "")
			pdf.Ln(2)

			for i, comment := range incident.Comments {
				pdf.SetFont("Arial", "B", 10)
				pdf.Cell(0, 6, fmt.Sprintf("%d. Comment", i+1))
				pdf.Ln(6)

				if comment.Author != nil {
					pdf.SetFont("Arial", "I", 9)
					pdf.SetTextColor(100, 100, 100)
					pdf.Cell(0, 5, fmt.Sprintf("By: %s %s - %s", comment.Author.FirstName, comment.Author.LastName, comment.CreatedAt.Format("2006-01-02 15:04:05")))
					pdf.Ln(5)
					pdf.SetTextColor(0, 0, 0)
				}

				pdf.SetFont("Arial", "", 10)
				pdf.MultiCell(0, 6, comment.Content, "", "L", false)

				if comment.IsInternal {
					pdf.SetFont("Arial", "I", 9)
					pdf.SetTextColor(200, 0, 0)
					pdf.Cell(0, 5, "[Internal Comment]")
					pdf.Ln(5)
					pdf.SetTextColor(0, 0, 0)
				}

				pdf.Ln(5)
			}
		}

		// Attachments Section
		if len(rawIncident.Attachments) > 0 {
			pdf.SetFont("Arial", "B", 14)
			pdf.SetFillColor(240, 240, 240)
			pdf.CellFormat(0, 8, fmt.Sprintf("Attachments (%d)", len(rawIncident.Attachments)), "", 1, "L", true, 0, "")
			pdf.Ln(2)

			for i, attachment := range rawIncident.Attachments {
				pdf.SetFont("Arial", "B", 10)
				pdf.Cell(0, 6, fmt.Sprintf("%d. %s", i+1, attachment.FileName))
				pdf.Ln(6)

				// Check if it's an image and try to embed it
				isImage := strings.HasPrefix(attachment.MimeType, "image/")
				imageEmbedded := false
				if isImage {
					// Try to fetch and embed the image
					fileReader, err := h.storage.GetFile(c.Context(), attachment.FilePath)
					if err == nil {
						// Read file into bytes
						imageData, err := io.ReadAll(fileReader)
						fileReader.Close()

						if err == nil && len(imageData) > 0 {
							// Determine image type
							imageType := ""
							if strings.Contains(attachment.MimeType, "jpeg") || strings.Contains(attachment.MimeType, "jpg") {
								imageType = "JPEG"
							} else if strings.Contains(attachment.MimeType, "png") {
								imageType = "PNG"
							} else if strings.Contains(attachment.MimeType, "gif") {
								imageType = "GIF"
							}

							if imageType != "" {
								// Register image from bytes
								imageKey := fmt.Sprintf("img_%s", attachment.ID.String())
								imageInfo := pdf.RegisterImageOptionsReader(imageKey, gofpdf.ImageOptions{ImageType: imageType}, bytes.NewReader(imageData))

								if imageInfo != nil {
									// Calculate dimensions to fit within page width
									pageWidth, _ := pdf.GetPageSize()
									maxWidth := pageWidth - 40.0 // Leave margins
									maxHeight := 80.0            // Max height for images

									// Calculate scaled dimensions maintaining aspect ratio
									imgWidth := imageInfo.Width()
									imgHeight := imageInfo.Height()
									scale := maxWidth / imgWidth
									scaledHeight := imgHeight * scale

									// Limit height
									if scaledHeight > maxHeight {
										scale = maxHeight / imgHeight
										scaledHeight = maxHeight
										maxWidth = imgWidth * scale
									}

									currentY := pdf.GetY()

									// Check if image fits on current page, add new page if needed
									_, pageHeight := pdf.GetPageSize()
									if currentY+scaledHeight > pageHeight-20 {
										pdf.AddPage()
										currentY = pdf.GetY()
									}

									// Add image with calculated dimensions
									pdf.ImageOptions(imageKey, 20, currentY, maxWidth, scaledHeight, false, gofpdf.ImageOptions{ImageType: imageType, ReadDpi: false}, 0, "")

									// Move cursor down by image height plus spacing
									pdf.SetY(currentY + scaledHeight + 5)
									imageEmbedded = true
								}
							}
						}
					}
				}

				// Show metadata (after image if embedded)
				if imageEmbedded {
					pdf.SetFont("Arial", "I", 9)
					pdf.SetTextColor(100, 100, 100)
					pdf.Cell(0, 5, "[Image Preview Above]")
					pdf.Ln(5)
					pdf.SetTextColor(0, 0, 0)
				}

				pdf.SetFont("Arial", "", 9)
				pdf.Cell(30, 5, "Type:")
				pdf.Cell(0, 5, attachment.MimeType)
				pdf.Ln(5)

				pdf.Cell(30, 5, "Size:")
				pdf.Cell(0, 5, fmt.Sprintf("%d bytes", attachment.FileSize))
				pdf.Ln(5)

				if attachment.UploadedBy != nil {
					pdf.Cell(30, 5, "Uploaded By:")
					pdf.Cell(0, 5, fmt.Sprintf("%s %s", attachment.UploadedBy.FirstName, attachment.UploadedBy.LastName))
					pdf.Ln(5)
				}

				pdf.Cell(30, 5, "Uploaded At:")
				pdf.Cell(0, 5, attachment.CreatedAt.Format("2006-01-02 15:04:05"))
				pdf.Ln(12)
			}
		}

		// Transition History Section
		if len(incident.TransitionHistory) > 0 {
			pdf.SetFont("Arial", "B", 14)
			pdf.SetFillColor(240, 240, 240)
			pdf.CellFormat(0, 8, fmt.Sprintf("Transition History (%d)", len(incident.TransitionHistory)), "", 1, "L", true, 0, "")
			pdf.Ln(2)

			for i, h := range incident.TransitionHistory {
				fromState := "Unknown"
				toState := "Unknown"
				if h.FromState != nil {
					fromState = h.FromState.Name
				}
				if h.ToState != nil {
					toState = h.ToState.Name
				}

				pdf.SetFont("Arial", "B", 10)
				pdf.Cell(0, 6, fmt.Sprintf("%d. %s → %s", i+1, fromState, toState))
				pdf.Ln(6)

				pdf.SetFont("Arial", "", 9)
				if h.PerformedBy != nil {
					pdf.Cell(30, 5, "By:")
					pdf.Cell(0, 5, fmt.Sprintf("%s %s", h.PerformedBy.FirstName, h.PerformedBy.LastName))
					pdf.Ln(5)
				}

				pdf.Cell(30, 5, "At:")
				pdf.Cell(0, 5, h.TransitionedAt.Format("2006-01-02 15:04:05"))
				pdf.Ln(5)

				if h.Comment != "" {
					pdf.Cell(30, 5, "Comment:")
					pdf.MultiCell(0, 5, h.Comment, "", "L", false)
					pdf.Ln(2)
				}

				pdf.Ln(5)
			}
		}

		// Generate PDF bytes
		var buf bytes.Buffer
		err = pdf.Output(&buf)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to generate PDF")
		}

		c.Set("Content-Type", "application/pdf")
		c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=incident_%s_%s.pdf",
			incident.IncidentNumber, time.Now().Format("20060102")))
		return c.Send(buf.Bytes())

	case "json":
		// Return JSON format
		c.Set("Content-Type", "application/json")
		c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=incident_%s_%s.json",
			incident.IncidentNumber, time.Now().Format("20060102")))
		return c.JSON(report)

	case "txt":
		// Return plain text format
		var buf bytes.Buffer
		buf.WriteString(fmt.Sprintf("INCIDENT REPORT\n"))
		buf.WriteString(fmt.Sprintf("===============\n\n"))
		buf.WriteString(fmt.Sprintf("Generated: %s\n\n", time.Now().Format("2006-01-02 15:04:05")))
		buf.WriteString(fmt.Sprintf("Incident Number: %s\n", incident.IncidentNumber))
		buf.WriteString(fmt.Sprintf("Title: %s\n", incident.Title))
		if incident.CurrentState != nil {
			buf.WriteString(fmt.Sprintf("Status: %s\n", incident.CurrentState.Name))
		}
		buf.WriteString(fmt.Sprintf("Record Type: %s\n", incident.RecordType))
		buf.WriteString(fmt.Sprintf("\n"))

		if incident.Description != "" {
			buf.WriteString(fmt.Sprintf("Description:\n%s\n\n", incident.Description))
		}

		if incident.Classification != nil {
			buf.WriteString(fmt.Sprintf("Classification: %s\n", incident.Classification.Name))
		}
		if incident.Department != nil {
			buf.WriteString(fmt.Sprintf("Department: %s\n", incident.Department.Name))
		}
		if incident.Assignee != nil {
			buf.WriteString(fmt.Sprintf("Assigned To: %s %s\n", incident.Assignee.FirstName, incident.Assignee.LastName))
		}
		if incident.Reporter != nil {
			buf.WriteString(fmt.Sprintf("Reported By: %s %s\n", incident.Reporter.FirstName, incident.Reporter.LastName))
		}

		buf.WriteString(fmt.Sprintf("\n"))
		buf.WriteString(fmt.Sprintf("Created: %s\n", incident.CreatedAt.Format("2006-01-02 15:04:05")))
		buf.WriteString(fmt.Sprintf("Updated: %s\n", incident.UpdatedAt.Format("2006-01-02 15:04:05")))

		if len(incident.Comments) > 0 {
			buf.WriteString(fmt.Sprintf("\n\nCOMMENTS (%d)\n", len(incident.Comments)))
			buf.WriteString(fmt.Sprintf("============\n\n"))
			for i, comment := range incident.Comments {
				buf.WriteString(fmt.Sprintf("%d. %s\n", i+1, comment.Content))
				if comment.Author != nil {
					buf.WriteString(fmt.Sprintf("   By: %s %s\n", comment.Author.FirstName, comment.Author.LastName))
				}
				buf.WriteString(fmt.Sprintf("   At: %s\n\n", comment.CreatedAt.Format("2006-01-02 15:04:05")))
			}
		}

		if len(incident.Attachments) > 0 {
			buf.WriteString(fmt.Sprintf("\n\nATTACHMENTS (%d)\n", len(incident.Attachments)))
			buf.WriteString(fmt.Sprintf("==============\n\n"))
			for i, attachment := range incident.Attachments {
				buf.WriteString(fmt.Sprintf("%d. %s\n", i+1, attachment.FileName))
				buf.WriteString(fmt.Sprintf("   Type: %s\n", attachment.MimeType))
				buf.WriteString(fmt.Sprintf("   Size: %d bytes\n\n", attachment.FileSize))
			}
		}

		if len(incident.TransitionHistory) > 0 {
			buf.WriteString(fmt.Sprintf("\n\nTRANSITION HISTORY (%d)\n", len(incident.TransitionHistory)))
			buf.WriteString(fmt.Sprintf("===================\n\n"))
			for i, h := range incident.TransitionHistory {
				fromState := "Unknown"
				toState := "Unknown"
				if h.FromState != nil {
					fromState = h.FromState.Name
				}
				if h.ToState != nil {
					toState = h.ToState.Name
				}
				buf.WriteString(fmt.Sprintf("%d. %s → %s\n", i+1, fromState, toState))
				if h.PerformedBy != nil {
					buf.WriteString(fmt.Sprintf("   By: %s %s\n", h.PerformedBy.FirstName, h.PerformedBy.LastName))
				}
				buf.WriteString(fmt.Sprintf("   At: %s\n", h.TransitionedAt.Format("2006-01-02 15:04:05")))
				if h.Comment != "" {
					buf.WriteString(fmt.Sprintf("   Comment: %s\n", h.Comment))
				}
				buf.WriteString("\n")
			}
		}

		c.Set("Content-Type", "text/plain")
		c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=incident_%s_%s.txt",
			incident.IncidentNumber, time.Now().Format("20060102")))
		return c.Send(buf.Bytes())

	default:
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid format. Use 'pdf', 'json', or 'txt'")
	}
}
