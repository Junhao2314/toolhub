-- Xiaping packages enter the same immutable artifact/review pipeline as other
-- imports, but retain their provider kind for provenance and UI filtering.
ALTER TABLE skill_sources DROP CONSTRAINT skill_sources_kind_check;
ALTER TABLE skill_sources ADD CONSTRAINT skill_sources_kind_check
    CHECK (kind IN ('upload','git','skillsmp','openai','node','xiaping'));
