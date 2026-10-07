<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Form\Type;

use MauticPlugin\MauticWhatsQrBundle\Domain\ConnectionName;
use Symfony\Component\Form\AbstractType;
use Symfony\Component\Form\Extension\Core\Type\ChoiceType;
use Symfony\Component\Form\Extension\Core\Type\TextType;
use Symfony\Component\Form\FormBuilderInterface;
use Symfony\Component\OptionsResolver\OptionsResolver;
use Symfony\Component\Validator\Constraints\Length;
use Symfony\Component\Validator\Constraints\NotBlank;
use Symfony\Component\Validator\Constraints\Regex;

final class QrConnectionType extends AbstractType
{
    public function buildForm(FormBuilderInterface $builder, array $options): void
    {
        $builder->add('name', TextType::class, [
            'label' => 'mautic.whatsqr.form.name', 'help' => 'mautic.whatsqr.form.name.help',
            'attr' => ['class' => 'form-control', 'maxlength' => ConnectionName::MAX_LENGTH, 'autocomplete' => 'off',
                'placeholder' => 'mautic.whatsqr.form.name.placeholder'],
            'constraints' => [new NotBlank(message: 'mautic.whatsqr.form.name.invalid'),
                new Length(max: ConnectionName::MAX_LENGTH, maxMessage: 'mautic.whatsqr.form.name.invalid'),
                new Regex(pattern: '/\p{Cc}/u', match: false, message: 'mautic.whatsqr.form.name.invalid')],
        ]);
        if (!$options['editing']) {
            $builder->add('source', ChoiceType::class, [
                'label' => 'mautic.whatsqr.form.source', 'help' => 'mautic.whatsqr.form.source.help',
                'attr' => ['class' => 'form-control'],
                'choices' => $options['sources'], 'invalid_message' => 'mautic.whatsqr.form.source.unavailable',
            ]);
        }
    }

    public function configureOptions(OptionsResolver $resolver): void
    {
        $resolver->setDefaults(['data_class' => null, 'editing' => false, 'sources' => []]);
        $resolver->setAllowedTypes('editing', 'bool');
        $resolver->setAllowedTypes('sources', 'array');
    }
}
