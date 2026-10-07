<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Form;

use MauticPlugin\MauticWhatsQrBundle\Form\Type\QrConnectionType;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Form\Extension\Csrf\CsrfExtension;
use Symfony\Component\Form\Extension\Validator\ValidatorExtension;
use Symfony\Component\Form\Forms;
use Symfony\Component\Form\FormInterface;
use Symfony\Component\Security\Csrf\CsrfTokenManager;
use Symfony\Component\Security\Csrf\TokenStorage\TokenStorageInterface;
use Symfony\Component\Validator\Validation;

final class QrConnectionTypeTest extends TestCase
{
    private function form(bool $editing = false, string $id = 'whatsqr_connection_new'): FormInterface
    {
        $tokens = $this->createMock(TokenStorageInterface::class);
        $tokens->method('hasToken')->willReturn(true);
        $tokens->method('getToken')->willReturn('unit-csrf-token');
        $factory = Forms::createFormFactoryBuilder()
            ->addExtension(new ValidatorExtension(Validation::createValidator()))
            ->addExtension(new CsrfExtension(new CsrfTokenManager(null, $tokens)))
            ->getFormFactory();

        return $factory->create(QrConnectionType::class, null, [
            'editing' => $editing, 'sources' => ['Company WhatsApp' => 16], 'csrf_token_id' => $id,
        ]);
    }

    public function testInvalidOrMissingCsrfCannotSave(): void
    {
        foreach (['', 'forged-token'] as $token) {
            $form = $this->form();
            $form->submit(['name' => 'Sales', 'source' => '16', '_token' => $token]);
            self::assertFalse($form->isValid());
        }
    }

    public function testNewFormAcceptsOnlyAnAvailableServerAndAValidName(): void
    {
        $valid = $this->form();
        $valid->submit(['name' => '  Vendas São Paulo  ', 'source' => '16', '_token' => 'unit-csrf-token']);
        self::assertTrue($valid->isValid(), (string) $valid->getErrors(true));
        self::assertSame('Vendas São Paulo', $valid->getData()['name']);
        foreach ([['Sales', '999'], ['', '16'], [str_repeat('a', 192), '16'], ["Sales\0bad", '16']] as [$name, $source]) {
            $form = $this->form();
            $form->submit(['name' => $name, 'source' => $source, '_token' => 'unit-csrf-token']);
            self::assertFalse($form->isValid());
        }
    }

    public function testEditAllowsOnlyTheNameAndRejectsAttemptsToReplaceTheDeviceOrSecrets(): void
    {
        $valid = $this->form(true, 'whatsqr_connection_edit_16');
        self::assertFalse($valid->has('source'));
        $valid->submit(['name' => 'Support', '_token' => 'unit-csrf-token']);
        self::assertTrue($valid->isValid());
        foreach (['external_id', 'settings', 'phone_number', 'source', 'token'] as $key) {
            $form = $this->form(true);
            $form->submit(['name' => 'Support', '_token' => 'unit-csrf-token', $key => 'tampered']);
            self::assertFalse($form->isValid());
        }
    }
}
