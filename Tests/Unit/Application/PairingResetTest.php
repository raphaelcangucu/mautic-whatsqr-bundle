<?php
declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;
use MauticPlugin\MauticWhatsQrBundle\Application\PairingScreen;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use PHPUnit\Framework\TestCase;
final class PairingResetTest extends TestCase
{
    public function testLiveAndAmbiguousCredentialsCannotBeErasedByReset(): void
    {
        $screen=new PairingScreen();
        foreach (['connected','pairing','reconnecting','ambiguous_credential'] as $status) {
            self::assertFalse($screen->canReset(new SessionState("testing-session", $status)), $status);
        }
        self::assertFalse($screen->canReset(new SessionState('testing-session', 'failed', reason: 'client_refused')));
        self::assertTrue($screen->canReset(new SessionState('testing-session', 'failed', reason: 'timeout')));
        self::assertTrue($screen->canReset(new SessionState('testing-session', 'logged_out')));
    }
}
