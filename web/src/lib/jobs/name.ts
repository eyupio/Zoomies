/** GitHub may send unexpanded names for jobs cancelled before matrix expansion.
 * Keep the source data intact; never invent a matrix value for display.
 */
export function readableJobText(text: string | null | undefined): string {
  return (text ?? '').replace(/\$\{\{\s*([^}]*?)\s*\}\}/g, (_, expression: string) => {
    return expression.trim() === 'matrix.projects' ? 'project unavailable' : 'value unavailable';
  });
}

export function unresolvedJobName(name: string | null | undefined): boolean {
  return readableJobText(name) !== (name ?? '');
}
