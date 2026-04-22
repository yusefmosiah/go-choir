const PRIMARY_DESKTOP_ID = 'primary';

function getWindowURL() {
  if (typeof window === 'undefined' || !window.location) return null;
  return new URL(window.location.href);
}

export function currentDesktopId() {
  const url = getWindowURL();
  if (!url) return '';
  const desktopId = (url.searchParams.get('desktop_id') || url.searchParams.get('desktop') || '').trim();
  return desktopId;
}

export function withDesktopSelector(input) {
  const desktopId = currentDesktopId();
  if (!desktopId || desktopId === PRIMARY_DESKTOP_ID) {
    return input;
  }

  const isAbsolute = /^[a-zA-Z][a-zA-Z\d+.-]*:/.test(input);
  const base = typeof window !== 'undefined' ? window.location.origin : 'http://localhost';
  const url = new URL(input, base);
  if (!url.pathname.startsWith('/api/')) {
    return input;
  }
  if (!url.searchParams.get('desktop_id')) {
    url.searchParams.set('desktop_id', desktopId);
  }
  if (isAbsolute) {
    return url.toString();
  }
  return `${url.pathname}${url.search}${url.hash}`;
}
