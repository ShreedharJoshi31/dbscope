interface NavigatorWithUAData extends Navigator {
  userAgentData?: { platform?: string };
}

export function isMacPlatform(): boolean {
  if (typeof navigator === "undefined") return false;
  const nav = navigator as NavigatorWithUAData;
  const platform = nav.userAgentData?.platform ?? nav.platform ?? "";
  return /mac|iphone|ipad/i.test(platform);
}
