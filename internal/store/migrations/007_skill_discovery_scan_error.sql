-- Shared-source skill scans report why a Skill failed package validation, but the
-- discovery row had nowhere to keep that reason: adoption_error belongs to the
-- admin-triggered adoption flow. Store the scan reason separately so the UI can
-- explain a blocked Skill instead of showing a bare drift flag.

ALTER TABLE skill_discoveries ADD COLUMN scan_error text NOT NULL DEFAULT '';
