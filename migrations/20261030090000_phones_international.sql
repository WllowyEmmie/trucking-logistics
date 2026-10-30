-- +goose Up

-- Drivers sign in with +234…; some were saved as typed (0803…, e.g. added
-- through the Copilot) and so could never sign in. Nigeria is the operator's
-- country; new numbers are converted by the API (auth.CanonicalPhone).
-- A number that would clash with another driver's is left for a person.
UPDATE drivers d
SET phone = '+234' || substr(d.phone, 2)
WHERE d.phone ~ '^0[0-9]{10}$'
  AND NOT EXISTS (SELECT 1 FROM drivers o WHERE o.phone = '+234' || substr(d.phone, 2));

UPDATE drivers d
SET phone = '+' || d.phone
WHERE d.phone ~ '^234[0-9]{10}$'
  AND NOT EXISTS (SELECT 1 FROM drivers o WHERE o.phone = '+' || d.phone);

-- +goose Down
-- Nothing to undo: the old form never worked for signing in.
SELECT 1;
