-- NULL means a legacy runner did not report capability metadata.
ALTER TABLE runner_sessions ADD COLUMN steering_supported boolean;
