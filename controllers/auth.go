package controllers

import (
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"time"

	_ "github.com/alastor-4/sylcot-go-gin-backend/docs"
	"github.com/alastor-4/sylcot-go-gin-backend/models"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthController struct {
	DB *gorm.DB
}

// Register godoc
// @Summary Register new user
// @Description Create a new user account
// @Tags authentication
// @Accept json
// @Produce json
// @Param user body models.User true "Registration data"
// @Success 201 {object} map[string]interface{} "message: User registered successfully..."
// @Failure 400 {object} map[string]interface{} "error: Validation failed, details: field errors"
// @Failure 409 {object} map[string]interface{} "error: User already exists"
// @Failure 500 {object} map[string]interface{} "error: Internal server error"
// @Router /auth/register [post]
func (ac *AuthController) Register(c *gin.Context) {
	var user models.User
	if err := c.ShouldBindJSON(&user); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request data",
			"details": map[string]interface{}{},
		})
		return
	}

	if err := user.Validate(); err != nil {
		validationErrors := models.GetValidationMessages(err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Validation failed",
			"details": validationErrors,
		})
		return
	}

	var existingUser models.User
	if err := ac.DB.Where("email = ?", user.Email).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "User with that email already registered"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error encrypting password"})
		return
	}

	verificationToken := uuid.NewString()

	newUser := models.User{
		Name:       user.Name,
		Email:      user.Email,
		Password:   string(hashedPassword),
		IsVerified: false,
		Token:      verificationToken,
	}

	if err := ac.DB.Create(&newUser).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not register the user"})
		return
	}

	verificationLink := "http://localhost:8080/auth/verify-email?token=" + verificationToken
	if err := sendVerificationEmail(user.Email, verificationLink); err != nil {
		log.Printf("Could not send verification email to %s: %v", user.Email, err)
	}

	fmt.Println(verificationToken)

	c.JSON(http.StatusCreated, gin.H{"message": "User registered successfully. Please verify your email."})
}

type LoginRequest struct {
	Email    string `json:"email" example:"user@example.com"`
	Password string `json:"password" example:"password123"`
}

// Login godoc
// @Summary User login
// @Description Authenticate user and return JWT token
// @Tags authentication
// @Accept json
// @Produce json
// @Param credentials body LoginRequest true "Login credentials"
// @Success 200 {object} map[string]interface{} "token: JWT string"
// @Failure 400 {object} map[string]interface{} "error: Invalid data"
// @Failure 401 {object} map[string]interface{} "error: Invalid credentials"
// @Failure 403 {object} map[string]interface{} "error: Email not verified"
// @Failure 500 {object} map[string]interface{} "error: Internal server error"
func (ac *AuthController) Login(c *gin.Context) {
	var loginData struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := c.ShouldBindJSON(&loginData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid data"})
		return
	}

	var user models.User
	if err := ac.DB.Where("email = ?", loginData.Email).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	}

	if !user.IsVerified {
		c.JSON(http.StatusForbidden, gin.H{"error": "Please verify your email first"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(loginData.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	}

	jwtToken, err := GenerateJWT(user.Email, int(user.ID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not generate JWT Token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": jwtToken})
}

// VerifyEmail godoc
// @Summary Verify user email
// @Description Validate email verification token
// @Tags authentication
// @Produce json
// @Param token query string true "Verification token"
// @Success 200 {object} map[string]interface{} "message: Verification success message"
// @Failure 400 {object} map[string]interface{} "error: Token required"
// @Failure 404 {object} map[string]interface{} "error: Invalid token"
// @Failure 500 {object} map[string]interface{} "error: Internal server error"
// @Router /auth/verify-email [get]
func (ac *AuthController) VerifyEmail(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token required"})
		return
	}
	var user models.User
	if err := ac.DB.Where("token = ?", token).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invalid token"})
		return
	}

	user.IsVerified = true
	user.Token = ""

	if err := ac.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error updating the user"})
		return
	}

	_, err := GenerateJWT(user.Email, int(user.ID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error generating JWT Token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("User with email %s verified successfully", user.Email),
	})
}

func getJWTExpiration() time.Duration {
	minutesStr := os.Getenv("JWT_EXPIRATION_MINUTES")
	if minutesStr == "" {
		return time.Minute * 4320
	}
	minutes, err := strconv.Atoi(minutesStr)
	if err != nil {
		return time.Minute * 4320
	}
	return time.Minute * time.Duration(minutes)
}

func GenerateJWT(email string, id int) (string, error) {
	secret := os.Getenv("JWT_SECRET")
	expiration := getJWTExpiration()
	claims := jwt.MapClaims{
		"email":  email,
		"userId": id,
		"iat":    time.Now().Unix(),
		"exp":    time.Now().Add(expiration).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func sendVerificationEmail(email, link string) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	from := os.Getenv("SMTP_USER")
	password := os.Getenv("SMTP_PASSWORD")
	subject := "Email Verification"
	body := "Click the following link to verify your email: " + link

	auth := smtp.PlainAuth("", from, password, smtpHost)
	msg := []byte("Subject: " + subject + "\r\n\r\n" + body)
	return smtp.SendMail(smtpHost+":"+smtpPort, auth, from, []string{email}, msg)
}
