-- Live updates: every audited change is announced on the hilo_events channel
-- once its transaction commits (rolled-back changes never notify). The
-- payload carries references only; listeners read the audit row back with the
-- workspace bound, so row-level security still decides what anyone sees.
CREATE FUNCTION notify_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.workspace_id IS NOT NULL THEN
        PERFORM pg_notify('hilo_events', json_build_object('id', NEW.id, 'ws', NEW.workspace_id)::text);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER audit_notify AFTER INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION notify_audit();
