-- Preserve existing stable keys while extending the curated identity palette.
ALTER TABLE sessions DROP CONSTRAINT sessions_color_check;
ALTER TABLE sessions ADD CONSTRAINT sessions_color_check
 CHECK (color IN ('', 'blue', 'orange', 'purple', 'teal', 'silver', 'indigo',
                 'gold', 'coral', 'cyan', 'violet', 'lime', 'rose'));
