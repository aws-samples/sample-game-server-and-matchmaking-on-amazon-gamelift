import type { RealtimeServerStackProps } from './realtime-server-stack';

export const DEFAULT_STACK_NAME = 'GameServerAndMatchmakingStack';
/** Keeps derived resource names within service limits. */
export const MAX_STACK_NAME_LENGTH = 40;

/** The retention periods, in days, that Amazon CloudWatch Logs accepts. */
export const LOG_RETENTION_DAYS = [1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653];

export interface ParsedContext {
  readonly stackName: string;
  readonly props: Omit<RealtimeServerStackProps, 'env'>;
}

/**
 * Reads and validates the -c context values passed to `cdk deploy`.
 * Throws an error that names the context key when a value is invalid.
 */
export function parseContext(get: (key: string) => unknown): ParsedContext {
  const text = (key: string): string | undefined => {
    const value = get(key);
    return value === undefined || value === null || value === '' ? undefined : String(value);
  };
  const flag = (key: string): boolean | undefined => {
    const value = text(key);
    if (value === undefined) return undefined;
    if (value === 'true' || value === 'false') return value === 'true';
    throw new Error(`Context value "${key}" must be true or false, got "${value}".`);
  };
  const integer = (key: string, min: number, max?: number): number | undefined => {
    const value = text(key);
    if (value === undefined) return undefined;
    const n = Number(value);
    if (!Number.isInteger(n) || n < min || (max !== undefined && n > max)) {
      throw new Error(`Context value "${key}" must be a whole number from ${min}${max !== undefined ? ` to ${max}` : ''}, got "${value}".`);
    }
    return n;
  };

  const retentionDays = (key: string): number | undefined => {
    const n = integer(key, 1, 3653);
    if (n !== undefined && !LOG_RETENTION_DAYS.includes(n)) {
      throw new Error(`Context value "${key}" must be one of ${LOG_RETENTION_DAYS.join(', ')}, got "${n}".`);
    }
    return n;
  };

  // -c allowedOrigins=http://127.0.0.1:8080,https://play.example.com (scheme, host, and optional port only)
  const origins = (key: string): string[] | undefined => {
    const value = text(key);
    if (value === undefined) return undefined;
    const items = value.split(',').map((item) => item.trim()).filter(Boolean);
    const bad = items.filter((item) => !/^https?:\/\/[A-Za-z0-9.-]+(:\d{1,5})?$/.test(item));
    if (!items.length || bad.length) {
      throw new Error(`Context value "${key}" must list origins such as http://127.0.0.1:8080, separated by commas; not allowed: ${bad.join(', ') || '(empty)'}.`);
    }
    return items;
  };

  // -c fleetType=anywhere (default: your own machine) or ec2 (a managed Amazon EC2 fleet)
  const fleetType = text('fleetType') ?? 'anywhere';
  if (fleetType !== 'anywhere' && fleetType !== 'ec2') {
    throw new Error(`Context value "fleetType" must be anywhere or ec2, got "${fleetType}".`);
  }
  // -c allowedCidr=203.0.113.7/32: who can reach the game port on the EC2 fleet. Required for ec2, with no
  // default, so the port is never open to the whole internet by accident.
  const allowedCidr = text('allowedCidr');
  if (fleetType === 'ec2') {
    const m = allowedCidr && /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(allowedCidr);
    if (!m || m.slice(1, 5).some((o) => Number(o) > 255) || Number(m[5]) > 32) {
      throw new Error('Context value "allowedCidr" must be an IPv4 range such as 203.0.113.7/32 when fleetType=ec2.');
    }
    if (Number(m[5]) === 0 && flag('allowPublic') !== true) {
      throw new Error('Context value "allowedCidr" opens the game port to every address. Use a narrower range, or also pass -c allowPublic=true if that is what you want.');
    }
  } else if (allowedCidr) {
    throw new Error('Context value "allowedCidr" is for fleetType=ec2. The Anywhere fleet opens no port.');
  }

  const stackName = text('stackName') ?? DEFAULT_STACK_NAME;
  if (!/^[A-Za-z][A-Za-z0-9-]*$/.test(stackName) || stackName.length > MAX_STACK_NAME_LENGTH) {
    throw new Error(`Context value "stackName" must start with a letter, use only letters, numbers, and hyphens, and be at most ${MAX_STACK_NAME_LENGTH} characters.`);
  }

  return {
    stackName,
    props: {
      logRetentionDays: retentionDays('logRetentionDays'),
      deleteLogsOnDestroy: flag('deleteLogsOnDestroy'),
      scoreToWin: integer('scoreToWin', 1, 20),
      allowedOrigins: origins('allowedOrigins'),
      fleetType: fleetType as 'anywhere' | 'ec2',
      allowedCidr,
    },
  };
}
