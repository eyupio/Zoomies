/**
 * What the controller hid from the model before it answered, said to the person.
 * Only counts come from the controller: what was hidden is never sent back.
 */
export function hiddenNote(credentials: number, emails: number): string {
  const parts: string[] = [];
  if (credentials > 0)
    parts.push(`${credentials} ${credentials === 1 ? 'credential' : 'credentials'}`);
  if (emails > 0) parts.push(`${emails} ${emails === 1 ? 'email address' : 'email addresses'}`);
  if (parts.length === 0) return '';
  return `${parts.join(' and ')} ${credentials + emails === 1 ? 'was' : 'were'} hidden before this went to the model.`;
}
