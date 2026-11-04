// Starting points for "Add OAuth provider": endpoints and scopes of common
// providers, so an admin only adds the client id and secret.
import type { OAuthProvider } from '../api'

const google = {
  issuer: 'https://accounts.google.com',
  authorization_endpoint: 'https://accounts.google.com/o/oauth2/v2/auth',
  token_endpoint: 'https://oauth2.googleapis.com/token',
  userinfo_endpoint: 'https://openidconnect.googleapis.com/v1/userinfo',
  revocation_endpoint: 'https://oauth2.googleapis.com/revoke',
}
const gscope = (...s: string[]) => ['openid', 'https://www.googleapis.com/auth/userinfo.email', ...s.map((x) => `https://www.googleapis.com/auth/${x}`)].join(' ')

export interface ProviderTemplate {
  key: string
  label: string
  description: string
  values: Partial<OAuthProvider>
}

export const providerTemplates: ProviderTemplate[] = [
  { key: 'google-login', label: 'Google sign-in', description: 'People sign in to this app with Google', values: { id: 'google', name: 'Google', login: true, ...google, scope: 'openid email profile' } },
  { key: 'google-sheets', label: 'Google Sheets', description: 'Per-user access for a Sheets tool server', values: { id: 'mcp:google-sheets', name: 'Google Sheets', ...google, scope: gscope('spreadsheets', 'drive.readonly', 'drive.file') } },
  { key: 'google-drive', label: 'Google Drive', description: 'Per-user access to Drive files', values: { id: 'mcp:google-drive', name: 'Google Drive', ...google, scope: gscope('drive.readonly', 'drive.file') } },
  { key: 'google-docs', label: 'Google Docs', description: 'Per-user access to Docs', values: { id: 'mcp:google-docs', name: 'Google Docs', ...google, scope: gscope('documents', 'drive.file') } },
  { key: 'gmail', label: 'Gmail', description: 'Read-only mailbox access', values: { id: 'mcp:gmail', name: 'Gmail', ...google, scope: gscope('gmail.readonly') } },
  { key: 'google-calendar', label: 'Google Calendar', description: 'Read and create calendar events', values: { id: 'mcp:google-calendar', name: 'Google Calendar', ...google, scope: gscope('calendar.events') } },
  {
    key: 'microsoft',
    label: 'Microsoft (Entra ID)',
    description: 'Work or school accounts; replace "common" with your tenant id to restrict',
    values: {
      id: 'microsoft',
      name: 'Microsoft',
      issuer: 'https://login.microsoftonline.com/common/v2.0',
      authorization_endpoint: 'https://login.microsoftonline.com/common/oauth2/v2.0/authorize',
      token_endpoint: 'https://login.microsoftonline.com/common/oauth2/v2.0/token',
      userinfo_endpoint: 'https://graph.microsoft.com/oidc/userinfo',
      revocation_endpoint: '',
      scope: 'openid email profile offline_access',
    },
  },
  {
    key: 'github',
    label: 'GitHub',
    description: 'GitHub OAuth app',
    values: {
      id: 'github',
      name: 'GitHub',
      issuer: 'https://github.com',
      authorization_endpoint: 'https://github.com/login/oauth/authorize',
      token_endpoint: 'https://github.com/login/oauth/access_token',
      userinfo_endpoint: 'https://api.github.com/user',
      revocation_endpoint: '',
      scope: 'read:user user:email',
    },
  },
  { key: 'custom', label: 'Custom', description: 'Any OAuth 2.0 / OpenID Connect provider', values: {} },
]
