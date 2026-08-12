import {
  createNestablePublicClientApplication,
  PublicClientApplication,
  InteractionRequiredAuthError,
  type IPublicClientApplication,
  type AuthenticationResult,
} from '@azure/msal-browser';

/**
 * Microsoft Graph tokens, acquired inside the taskpane with no backend.
 *
 * Nested App Authentication is what makes this add-in serverless. The older
 * Office SSO path (`Office.context.auth.getAccessToken`) hands back a token for
 * *your* app that you then have to exchange for a Graph token via on-behalf-of,
 * and OBO requires a client secret, which requires a server. NAA skips the
 * exchange: MSAL running in the taskpane asks the Outlook host for a Graph
 * token directly. NAA is also no longer optional - legacy Exchange user identity
 * and callback tokens were switched off for Outlook add-ins in February 2025.
 */

/**
 * Scopes. Every one of these is user-consentable: none requires an admin, on
 * either work or personal accounts.
 *  - Mail.ReadWrite ............ read messages, PATCH the categories property
 *  - MailboxSettings.ReadWrite . create the mailbox master category list
 *  - User.Read ................. resolve who is signed in, for the UI
 *
 * MailboxSettings.ReadWrite is also what covers reading and writing
 * /me/mailFolders/inbox/messageRules, which is how confident senders get
 * promoted into native Outlook rules.
 */
export const GRAPH_SCOPES = [
  'https://graph.microsoft.com/Mail.ReadWrite',
  'https://graph.microsoft.com/MailboxSettings.ReadWrite',
  'https://graph.microsoft.com/User.Read',
];

/**
 * Entra application (client) ID. Set at build time; see README "Register the app".
 * Left empty the add-in still loads and can explain itself, which is better
 * than a blank pane.
 */
const CLIENT_ID = (import.meta.env?.VITE_ENTRA_CLIENT_ID as string | undefined) ?? '';

/**
 * `/common` is required: it is the only authority that accepts both work/school
 * and personal Microsoft accounts. A tenant GUID or `/organizations` would lock
 * out personal mailboxes.
 */
const AUTHORITY = 'https://login.microsoftonline.com/common';

export class AuthUnavailableError extends Error {}

let appPromise: Promise<IPublicClientApplication> | null = null;
let usingNaa = false;

/** True when the host advertises NAA support. Cheap, so callers may re-check. */
export function naaSupported(): boolean {
  try {
    return Office.context.requirements.isSetSupported('NestedAppAuth', '1.1');
  } catch {
    return false;
  }
}

function msalConfig() {
  return {
    auth: {
      clientId: CLIENT_ID,
      authority: AUTHORITY,
      // NAA's redirect URI is origin-only, with no path:
      //   brk-multihub://<your-origin>
      // It is registered in Entra as an SPA redirect. Note this authorizes the
      // whole origin, which is why the README insists on a dedicated domain
      // rather than a shared *.github.io subdomain.
      redirectUri: `brk-multihub://${location.host}`,
    },
    cache: {
      // MSAL's own token cache. Distinct from our application state, which
      // lives in roamingSettings.
      cacheLocation: 'localStorage' as const,
    },
  };
}

async function getApp(): Promise<IPublicClientApplication> {
  if (!CLIENT_ID) {
    throw new AuthUnavailableError(
      'No Entra client ID was built into this add-in. Set VITE_ENTRA_CLIENT_ID and redeploy.',
    );
  }
  if (appPromise) return appPromise;

  appPromise = (async () => {
    if (naaSupported()) {
      usingNaa = true;
      return createNestablePublicClientApplication(msalConfig());
    }
    // Older host with no NAA. A plain MSAL public client in the taskpane still
    // works via popup; the user just sees an explicit sign-in the first time.
    usingNaa = false;
    const app = new PublicClientApplication(msalConfig());
    await app.initialize();
    return app;
  })();

  return appPromise;
}

/** Which mechanism actually got used, for the diagnostics line in settings. */
export function authMode(): 'naa' | 'popup' | 'unknown' {
  if (!appPromise) return 'unknown';
  return usingNaa ? 'naa' : 'popup';
}

/**
 * A Graph bearer token, silently where possible.
 *
 * NAA is popup-only: acquireTokenRedirect, loginRedirect, logout*, and
 * handleRedirectPromise all throw under NAA, so there is deliberately no
 * redirect path here.
 */
export async function getGraphToken(): Promise<string> {
  if (import.meta.env.DEV) {
    // The dev harness supplies a stand-in token; see src/dev-mock.ts.
    const devToken = (globalThis as Record<string, unknown>).__INBOX_STEWARD_DEV_TOKEN__;
    if (typeof devToken === 'string') return devToken;
  }

  const app = await getApp();
  const request = { scopes: GRAPH_SCOPES };

  let result: AuthenticationResult;
  try {
    const accounts = app.getAllAccounts();
    result = await app.acquireTokenSilent(
      accounts.length > 0 ? { ...request, account: accounts[0] } : request,
    );
  } catch (err) {
    if (err instanceof InteractionRequiredAuthError) {
      result = await app.acquireTokenPopup(request);
    } else {
      throw err;
    }
  }

  if (!result.accessToken) {
    throw new AuthUnavailableError('Sign-in succeeded but no access token was returned.');
  }
  return result.accessToken;
}

/** Display name / address of the signed-in user, best effort. */
export async function signedInAs(): Promise<string | null> {
  try {
    const app = await getApp();
    const account = app.getAllAccounts()[0];
    return account?.username ?? null;
  } catch {
    return null;
  }
}
