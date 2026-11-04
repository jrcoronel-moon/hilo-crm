-- Joining a workspace needs the person's acceptance: an admin cannot pull
-- someone in. Only invitations sent by the operator (hiloctl), who already
-- holds the database, are accepted on the person's first sign-in.
ALTER TABLE invitations ADD COLUMN preaccepted boolean NOT NULL DEFAULT false;
