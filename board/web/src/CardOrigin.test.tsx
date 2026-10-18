import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { CardOrigin, RestrictedMarker } from './CardOrigin';

describe('card provenance and restricted details', () => {
  it('shows the originating session, repo, commit and engine as separate fields', () => {
    render(<CardOrigin origin={{ session_url: 'https://runner.example.test/sessions/fixture', repo: 'example/app', commit: 'abcdef1', engine: 'codex', component_version: '0.4.0', component_commit: 'fedcba9' }} />);
    expect(screen.getByRole('link', { name: 'Originating session' })).toHaveAttribute('href', 'https://runner.example.test/sessions/fixture');
    for (const text of ['example/app', 'abcdef1', 'codex', '0.4.0', 'fedcba9']) expect(screen.getByText(text)).toBeInTheDocument();
  });
  it('never turns an untrusted session URL into an executable link', () => {
    render(<CardOrigin origin={{ session_url: 'javascript:alert(1)', repo: '', commit: '', engine: '' }} />);
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });
  it('shows a restricted marker without rendering reproduction details', () => {
    const { rerender } = render(<RestrictedMarker sensitive />);
    expect(screen.getByText('Restricted details')).toBeInTheDocument();
    rerender(<RestrictedMarker sensitive={false} />);
    expect(screen.queryByText('Restricted details')).not.toBeInTheDocument();
  });
});
